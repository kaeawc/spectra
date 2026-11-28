//! Synthetic binaries, an in-memory filesystem and fakes (Go's `fixtures_test.go`).

use std::collections::{BTreeMap, HashMap, HashSet};
use std::io;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use spectra_base::fs::{DirEntry, FileInfo, FileType, Fs, not_regular};
use spectra_base::hostos::Kind;
use spectra_base::proc::{CancelToken, Cmd, FakeRunner, Output, RunError, Runner, match_prefix};

use crate::classify::macho::{CPU_AMD64, CPU_ARM64, MAGIC_FAT, MAGIC64};
use crate::classify::{Classification, Classifier, Input, Options, Store};

pub const CPU_ARM: u32 = CPU_ARM64;
pub const CPU_X86: u32 = CPU_AMD64;

/// One section of a synthetic binary: segment, name, data.
pub struct Sect<'a>(pub &'a str, pub &'a str, pub &'a [u8]);

fn u32s(out: &mut Vec<u8>, v: &[u32]) {
    for x in v {
        out.extend_from_slice(&x.to_le_bytes());
    }
}

fn u64s(out: &mut Vec<u8>, v: &[u64]) {
    for x in v {
        out.extend_from_slice(&x.to_le_bytes());
    }
}

fn name16(s: &str) -> [u8; 16] {
    let mut b = [0u8; 16];
    b[..s.len()].copy_from_slice(s.as_bytes());
    b
}

/// Go's `buildMachO`: a minimal 64-bit Mach-O executable whose sections hold the data; one
/// LC_SEGMENT_64 per distinct segment.
pub fn build_macho(cpu: u32, sects: &[Sect<'_>]) -> Vec<u8> {
    let mut segs: Vec<&str> = Vec::new();
    for s in sects {
        if !segs.contains(&s.0) {
            segs.push(s.0);
        }
    }
    let by_seg = |seg: &str| sects.iter().filter(move |s| s.0 == seg).collect::<Vec<_>>();
    const HDR: usize = 32;
    const SEG_CMD: usize = 72;
    const SECT_HDR: usize = 80;
    let cmds_size: usize = segs.iter().map(|s| SEG_CMD + SECT_HDR * by_seg(s).len()).sum();
    let data_off = HDR + cmds_size;
    let (mut cmds, mut data) = (Vec::new(), Vec::new());
    for seg in &segs {
        let ss = by_seg(seg);
        let seg_start = data_off + data.len();
        let seg_size: usize = ss.iter().map(|s| s.2.len()).sum();
        u32s(&mut cmds, &[0x19, (SEG_CMD + SECT_HDR * ss.len()) as u32]);
        cmds.extend_from_slice(&name16(seg));
        u64s(&mut cmds, &[0x1000, seg_size as u64, seg_start as u64, seg_size as u64]);
        u32s(&mut cmds, &[7, 5, ss.len() as u32, 0]);
        for s in ss {
            let off = data_off + data.len();
            cmds.extend_from_slice(&name16(s.1));
            cmds.extend_from_slice(&name16(seg));
            u64s(&mut cmds, &[0x1000, s.2.len() as u64]);
            u32s(&mut cmds, &[off as u32, 0, 0, 0, 0, 0, 0, 0]);
            data.extend_from_slice(s.2);
        }
    }
    let mut out = Vec::new();
    u32s(&mut out, &[MAGIC64, cpu, 0, 2, segs.len() as u32, cmds_size as u32, 0, 0]);
    out.extend(cmds);
    out.extend(data);
    out
}

/// Go's `buildFat`: thin slices wrapped in a universal binary, in `order`.
pub fn build_fat(slices: &[(u32, Vec<u8>)]) -> Vec<u8> {
    let mut out = Vec::new();
    for v in [MAGIC_FAT, slices.len() as u32] {
        out.extend_from_slice(&v.to_be_bytes());
    }
    let mut cur = (8 + 20 * slices.len() + 15) & !15;
    let mut offsets = Vec::new();
    for (_, s) in slices {
        offsets.push(cur);
        cur += (s.len() + 15) & !15;
    }
    for (i, (cpu, s)) in slices.iter().enumerate() {
        for v in [*cpu, 0, offsets[i] as u32, s.len() as u32, 4] {
            out.extend_from_slice(&v.to_be_bytes());
        }
    }
    for (i, (_, s)) in slices.iter().enumerate() {
        out.resize(offsets[i], 0);
        out.extend_from_slice(s);
    }
    out
}

fn elf_header(out: &mut Vec<u8>, shoff: usize, shnum: u16) {
    out.extend_from_slice(&[0x7f, b'E', b'L', b'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
    for v in [2u16, 62] {
        out.extend_from_slice(&v.to_le_bytes());
    }
    u32s(out, &[1]);
    u64s(out, &[0, 0, shoff as u64]);
    u32s(out, &[0]);
    for v in [64u16, 56, 0, 64, shnum, shnum - 1] {
        out.extend_from_slice(&v.to_le_bytes());
    }
}

#[allow(clippy::too_many_arguments)] // one ELF section header, field by field
fn shdr(out: &mut Vec<u8>, name: u32, typ: u32, off: usize, size: usize, link: u32, info: u32, align: u64, entsize: u64) {
    u32s(out, &[name, typ]);
    u64s(out, &[2, 0, off as u64, size as u64]);
    u32s(out, &[link, info]);
    u64s(out, &[align, entsize]);
}

/// Go's `buildELF`: a minimal 64-bit little-endian ELF with the named PROGBITS sections.
pub fn build_elf(sects: &[(&str, &[u8])]) -> Vec<u8> {
    let mut shstr = vec![0u8];
    let mut name_off = Vec::new();
    for (name, _) in sects {
        name_off.push(shstr.len());
        shstr.extend_from_slice(name.as_bytes());
        shstr.push(0);
    }
    let shstr_name = shstr.len();
    shstr.extend_from_slice(b".shstrtab\0");
    let mut data = Vec::new();
    let mut offs = Vec::new();
    for (_, d) in sects {
        offs.push(64 + data.len());
        data.extend_from_slice(d);
    }
    let shstr_off = 64 + data.len();
    data.extend_from_slice(&shstr);
    while !data.len().is_multiple_of(8) {
        data.push(0);
    }
    let shoff = 64 + data.len();
    let shnum = sects.len() + 2;
    let mut out = Vec::new();
    elf_header(&mut out, shoff, shnum as u16);
    out.extend(data);
    out.extend_from_slice(&[0u8; 64]);
    for (i, (_, d)) in sects.iter().enumerate() {
        shdr(&mut out, name_off[i] as u32, 1, offs[i], d.len(), 0, 0, 1, 0);
    }
    shdr(&mut out, shstr_name as u32, 3, shstr_off, shstr.len(), 0, 0, 1, 0);
    out
}

/// One function symbol of [`build_elf_with_symtab`]; `local` is STB_LOCAL.
pub struct ElfSym<'a>(pub &'a str, pub bool);

/// Go's `buildELFWithSymtab`: `.rodata` and an unstripped `.symtab` of function symbols
/// (locals first).
pub fn build_elf_with_symtab(rodata: &[u8], syms: &[ElfSym<'_>]) -> Vec<u8> {
    let mut strtab = vec![0u8];
    let mut symtab = vec![0u8; 24];
    let mut first_global = 1;
    for (i, s) in syms.iter().enumerate() {
        let info = if s.1 {
            first_global = i + 2;
            0x02
        } else {
            0x12
        };
        u32s(&mut symtab, &[strtab.len() as u32]);
        symtab.extend_from_slice(&[info, 0]);
        symtab.extend_from_slice(&1u16.to_le_bytes());
        u64s(&mut symtab, &[0x1000, 16]);
        strtab.extend_from_slice(s.0.as_bytes());
        strtab.push(0);
    }
    let shstr = b"\x00.rodata\x00.symtab\x00.strtab\x00.shstrtab\x00";
    let mut data = Vec::new();
    let pad = |d: &mut Vec<u8>| {
        while !d.len().is_multiple_of(8) {
            d.push(0);
        }
    };
    let rodata_off = 64 + data.len();
    data.extend_from_slice(rodata);
    pad(&mut data);
    let sym_off = 64 + data.len();
    data.extend_from_slice(&symtab);
    let str_off = 64 + data.len();
    data.extend_from_slice(&strtab);
    let shstr_off = 64 + data.len();
    data.extend_from_slice(shstr);
    pad(&mut data);
    let shoff = 64 + data.len();
    let mut out = Vec::new();
    elf_header(&mut out, shoff, 5);
    out.extend(data);
    out.extend_from_slice(&[0u8; 64]);
    shdr(&mut out, 1, 1, rodata_off, rodata.len(), 0, 0, 8, 0);
    shdr(&mut out, 9, 2, sym_off, symtab.len(), 3, first_global as u32, 8, 24);
    shdr(&mut out, 17, 3, str_off, strtab.len(), 0, 0, 8, 0);
    shdr(&mut out, 25, 3, shstr_off, shstr.len(), 0, 0, 8, 0);
    out
}

/// Go's `symtabOnlyMachO`.
pub fn symtab_only_macho(nsyms: u32, strsize: u32) -> Vec<u8> {
    let mut b = Vec::new();
    u32s(&mut b, &[MAGIC64, CPU_ARM, 0, 2, 1, 24, 0, 0]);
    u32s(&mut b, &[0x2, 24, 4096, nsyms, 8192, strsize]);
    b
}

pub fn rust_data() -> Vec<u8> {
    b"xx/rustc/90b35a6239c3d8bdabc530a6a0816f7ff89a0aaf/library/core/src/panicking.rs\x00RUST_BACKTRACE\x00".to_vec()
}

pub fn info_plist(id: &str, exe: &str) -> Vec<u8> {
    format!(
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict>\n<key>CFBundleIdentifier</key><string>{id}</string>\n<key>CFBundleExecutable</key><string>{exe}</string>\n</dict></plist>"
    )
    .into_bytes()
}

/// One file of a [`FakeFs`]: its bytes and the stat fields the cache keys on.
#[derive(Clone)]
pub struct FakeFile {
    pub data: Vec<u8>,
    pub mtime: i64,
    pub ino: u64,
    pub ctime: i64,
}

/// Go's `fakeFS`: files at absolute paths (directories implied), reads that can be denied,
/// EvalSymlinks results, and an optional cancel on the first Info.plist read (Go's
/// `cancellingFS`).
#[derive(Default)]
pub struct FakeFs {
    pub files: Mutex<BTreeMap<String, FakeFile>>,
    pub deny: Mutex<HashSet<String>>,
    pub links: Mutex<BTreeMap<String, String>>,
    pub cancel_on_plist: Option<CancelToken>,
    pub plist_reads: AtomicUsize,
}

/// Go's `fixtureTime` in Unix nanoseconds.
pub const FIXTURE_TIME: i64 = 1_767_323_045_000_000_000;

impl FakeFs {
    pub fn new() -> Arc<FakeFs> {
        Arc::new(FakeFs::default())
    }

    pub fn add(&self, path: &str, data: impl Into<Vec<u8>>) {
        self.files.lock().expect("lock").insert(path.into(), FakeFile { data: data.into(), mtime: FIXTURE_TIME, ino: 0, ctime: 0 });
    }

    pub fn set(&self, path: &str, f: FakeFile) {
        self.files.lock().expect("lock").insert(path.into(), f);
    }

    pub fn deny(&self, path: &str, on: bool) {
        let mut d = self.deny.lock().expect("lock");
        if on {
            d.insert(path.into());
        } else {
            d.remove(path);
        }
    }

    pub fn link(&self, from: &str, to: &str) {
        self.links.lock().expect("lock").insert(from.into(), to.into());
    }

    fn key(path: &Path) -> String {
        path.to_string_lossy().into_owned()
    }

    fn file(&self, path: &Path) -> Option<FakeFile> {
        self.files.lock().expect("lock").get(&Self::key(path)).cloned()
    }

    fn is_dir(&self, path: &Path) -> bool {
        let prefix = format!("{}/", Self::key(path).trim_end_matches('/'));
        self.files.lock().expect("lock").keys().any(|k| k.starts_with(&prefix))
    }
}

fn not_found() -> io::Error {
    io::Error::from_raw_os_error(2)
}

fn info(file_type: FileType, f: Option<&FakeFile>) -> FileInfo {
    let (len, mtime, ino, ctime) = f.map_or((0, 0, 0, 0), |f| (f.data.len() as u64, f.mtime, f.ino, f.ctime));
    let split = |ns: i64| Some((ns.div_euclid(1_000_000_000), ns.rem_euclid(1_000_000_000) as u32));
    FileInfo { file_type, len, mode: 0o755, modified: split(mtime), dev: 0, ino, ctime: if ctime == 0 { None } else { split(ctime) } }
}

impl Fs for FakeFs {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        if self.deny.lock().expect("lock").contains(&Self::key(path)) {
            return Err(io::Error::from_raw_os_error(13));
        }
        if path.ends_with("Info.plist") {
            self.plist_reads.fetch_add(1, Ordering::SeqCst);
            if let Some(c) = &self.cancel_on_plist {
                c.cancel();
            }
        }
        match self.file(path) {
            Some(f) => Ok(f.data),
            None if self.is_dir(path) => Err(not_regular()),
            None => Err(not_found()),
        }
    }

    fn read_dir(&self, _: &Path) -> io::Result<Vec<DirEntry>> {
        Err(io::Error::other("FakeFs: read_dir is not supported"))
    }

    fn metadata(&self, path: &Path) -> io::Result<FileInfo> {
        if let Some(f) = self.file(path) {
            return Ok(info(FileType::File, Some(&f)));
        }
        let target = self.links.lock().expect("lock").get(&Self::key(path)).cloned();
        if let Some(t) = target {
            return self.metadata(Path::new(&t));
        }
        if self.is_dir(path) || path == Path::new("/") {
            return Ok(info(FileType::Dir, None));
        }
        Err(not_found())
    }

    fn symlink_metadata(&self, path: &Path) -> io::Result<FileInfo> {
        if self.links.lock().expect("lock").contains_key(&Self::key(path)) {
            return Ok(info(FileType::Symlink, None));
        }
        self.metadata(path)
    }

    fn read_link(&self, path: &Path) -> io::Result<PathBuf> {
        self.links.lock().expect("lock").get(&Self::key(path)).map(PathBuf::from).ok_or_else(not_found)
    }
}

/// Go's `memStore`: an in-memory [`Store`] that counts traffic.
#[derive(Default)]
pub struct MemStore {
    pub data: Mutex<HashMap<Vec<u8>, Vec<u8>>>,
    pub gets: AtomicUsize,
    pub puts: AtomicUsize,
}

impl MemStore {
    pub fn new() -> Arc<MemStore> {
        Arc::new(MemStore::default())
    }

    pub fn puts(&self) -> usize {
        self.puts.load(Ordering::SeqCst)
    }
}

impl Store for MemStore {
    fn get(&self, key: &[u8]) -> io::Result<Option<Vec<u8>>> {
        self.gets.fetch_add(1, Ordering::SeqCst);
        Ok(self.data.lock().expect("lock").get(key).cloned())
    }

    fn put(&self, key: &[u8], data: &[u8]) -> io::Result<()> {
        self.puts.fetch_add(1, Ordering::SeqCst);
        self.data.lock().expect("lock").insert(key.to_vec(), data.to_vec());
        Ok(())
    }
}

pub fn stderr(s: &str, exit_code: i32) -> Output {
    Output { stdout: Vec::new(), stderr: s.as_bytes().to_vec(), exit_code }
}

/// Go's `codesignFake`: every codesign call answers `out` on stderr.
pub fn codesign_fake(out: &str) -> Arc<FakeRunner> {
    let f = FakeRunner::new();
    f.on(match_prefix("codesign", &[]), stderr(out, 0));
    Arc::new(f)
}

/// Go's `flakyRunner`: fails every call while `fail` is set.
pub struct FlakyRunner {
    pub inner: Arc<FakeRunner>,
    pub fail: AtomicBool,
}

impl Runner for FlakyRunner {
    fn run(&self, cmd: &Cmd) -> Result<Output, RunError> {
        if self.fail.load(Ordering::SeqCst) {
            return Err(RunError::TimedOut { cmd: cmd.display(), after: std::time::Duration::from_secs(10) });
        }
        self.inner.run(cmd)
    }
}

pub fn opts(os: Kind, fs: Arc<dyn Fs>, runner: Arc<dyn Runner>) -> Options {
    Options { os, runner, fs, store: None, workers: 0, max_scan_bytes: 0, proc_fs: String::new(), arch: "arm64".into() }
}

pub fn with_store(mut o: Options, store: Arc<MemStore>) -> Options {
    o.store = Some(store);
    o
}

pub fn no_argv(_: usize) -> Vec<String> {
    Vec::new()
}

/// Go's `classifyOne`.
pub fn classify_one(c: &Classifier, input: Input) -> Classification {
    let (res, err) = c.classify(&[input], &no_argv, &CancelToken::new());
    assert!(err.is_none(), "classify: {err:?}");
    res.into_iter().next().expect("one result")
}

pub fn input(pid: i64, uid: i64, command: &str, exe: &str, cmdline: &str) -> Input {
    Input { pid, ppid: 1, uid, command: command.into(), executable_path: exe.into(), command_line: cmdline.into(), ..Input::default() }
}

pub fn evidence(r: &Classification) -> String {
    r.evidence.join("\n")
}

pub fn calls(f: &FakeRunner) -> usize {
    f.calls().len()
}
