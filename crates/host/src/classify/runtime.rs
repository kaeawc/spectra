//! Runtime detection from names and from the executable's bytes (Go's `runtime.go`).

use serde::{Deserialize, Serialize};
use spectra_base::fs::Fs;
use spectra_base::gojson;

use super::files::open_regular;
use super::reader::{ReadAt, Section, read_full_at};
use super::{Runtime, buildinfo, elf, macho, shell_name};

/// Go's `interpreterRuntime`: interpreters and managed runtimes by executable base name (or
/// argv[0] when the executable is not visible).
pub fn interpreter_runtime(base: &str) -> Option<Runtime> {
    let name = base.strip_prefix('-').unwrap_or(base).to_lowercase();
    let versioned = |prefix: &str| name.strip_prefix(prefix).is_some_and(|v| v.bytes().all(|c| c.is_ascii_digit() || c == b'.'));
    match name.as_str() {
        "java" => Some(Runtime::Jvm),
        "node" | "nodejs" => Some(Runtime::Node),
        "bun" => Some(Runtime::Bun),
        "deno" => Some(Runtime::Deno),
        _ if versioned("python") => Some(Runtime::Python),
        _ if versioned("ruby") => Some(Runtime::Ruby),
        n if shell_name(n) => Some(Runtime::Shell),
        _ => None,
    }
}

/// The result of inspecting one executable file (Go's `binaryVerdict`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct BinaryVerdict {
    pub runtime: Runtime,
    pub evidence: String,
    /// The main module path from Go build info, when present.
    #[serde(default, skip_serializing_if = "gojson::is_empty")]
    pub go_module: String,
}

impl BinaryVerdict {
    fn new(runtime: Runtime, evidence: impl Into<String>) -> Self {
        BinaryVerdict { runtime, evidence: evidence.into(), go_module: String::new() }
    }
}

/// Strings rustc's standard library leaves in read-only data even in stripped release builds.
const RUST_NEEDLES: [&[u8]; 2] = [b"/rustc/", b"RUST_BACKTRACE"];

/// `std::rt::lang_start` in both manglings; the length prefix keeps `lang_start_internal` out.
const RUST_ENTRY_SYMBOL: &str = "3std2rt10lang_start";

fn unreadable(err: &str) -> BinaryVerdict {
    BinaryVerdict::new(Runtime::Unknown, format!("executable unreadable: {err}"))
}

/// Go's `inspectBinary`. An error means the file could not be read (a race with exit or
/// replacement, a permission error): the verdict is then unknown and not worth caching. `arch`
/// is Go's GOARCH for picking a universal binary's slice.
pub fn inspect_binary(fs: &dyn Fs, path: &str, max_scan: i64, arch: &str) -> (BinaryVerdict, Option<String>) {
    let f = match open_regular(fs, path) {
        Ok(f) => f,
        Err(e) => return (unreadable(&e), Some(e)),
    };
    let mut magic = [0u8; 4];
    if let Err(e) = read_full_at(&f, 0, &mut magic) {
        return (unreadable(&e), Some(e));
    }
    if u32::from_be_bytes(magic) == macho::MAGIC_FAT {
        return (inspect_fat(&f, max_scan, arch), None);
    }
    let mut v = if let Some((mr, skipped)) = bound_symtab(&f, max_scan) {
        match macho::parse(&mr) {
            Ok(mf) => macho_verdict(&mf, &mr, max_scan, skipped),
            Err(e) => return (BinaryVerdict::new(Runtime::Unknown, format!("malformed Mach-O: {e}")), None),
        }
    } else if let Ok(ef) = elf::parse(&f) {
        elf_verdict(&ef, &f, max_scan)
    } else {
        return (BinaryVerdict::new(Runtime::Unknown, "executable is neither Mach-O nor ELF"), None);
    };
    if v.runtime == Runtime::Go {
        v.go_module = buildinfo::go_module(&f);
    }
    (v, None)
}

/// A fat slice's CPU and where it lies.
#[derive(Clone, Copy, Debug)]
pub struct FatArch {
    pub cpu: u32,
    pub offset: u64,
    pub size: u64,
}

/// Go's `inspectFat`: only the slice the host would run.
fn inspect_fat(f: &dyn ReadAt, max_scan: i64, arch: &str) -> BinaryVerdict {
    let arches = match read_fat_arches(f) {
        Ok(a) => a,
        Err(e) => return BinaryVerdict::new(Runtime::Unknown, format!("malformed universal binary: {e}")),
    };
    let slice = pick_fat_slice(&arches, arch);
    let sr = Section { r: f, off: slice.offset, size: slice.size };
    let Some((mr, skipped)) = bound_symtab(&sr, max_scan) else {
        return BinaryVerdict::new(
            Runtime::Unknown,
            format!("malformed universal binary: {} slice is not Mach-O", macho::cpu_name(slice.cpu)),
        );
    };
    let mf = match macho::parse(&mr) {
        Ok(mf) => mf,
        Err(e) => return BinaryVerdict::new(Runtime::Unknown, format!("malformed universal binary: {e}")),
    };
    let mut v = macho_verdict(&mf, &mr, max_scan, skipped);
    if v.runtime == Runtime::Go {
        v.go_module = buildinfo::go_module(&sr);
    }
    v.evidence.push_str(&format!(" (universal binary, {} slice)", macho::cpu_name(slice.cpu)));
    v
}

/// Go's `readFatArches`.
pub fn read_fat_arches(r: &dyn ReadAt) -> Result<Vec<FatArch>, String> {
    let mut hdr = [0u8; 8];
    read_full_at(r, 0, &mut hdr).map_err(|e| format!("read fat header: {e}"))?;
    let n = u32::from_be_bytes([hdr[4], hdr[5], hdr[6], hdr[7]]);
    if n == 0 || n > 64 {
        return Err(format!("implausible architecture count {n}"));
    }
    let mut buf = vec![0u8; 20 * n as usize];
    read_full_at(r, 8, &mut buf).map_err(|e| format!("read fat architectures: {e}"))?;
    Ok(buf
        .as_chunks::<20>()
        .0
        .iter()
        .map(|e| {
            let be = |i: usize| u32::from_be_bytes([e[i], e[i + 1], e[i + 2], e[i + 3]]);
            FatArch { cpu: be(0), offset: u64::from(be(8)), size: u64::from(be(12)) }
        })
        .collect())
}

/// Go's `pickFatSlice`: the host architecture's slice, else the first.
pub fn pick_fat_slice(arches: &[FatArch], arch: &str) -> FatArch {
    let want = if arch == "amd64" { macho::CPU_AMD64 } else { macho::CPU_ARM64 };
    arches.iter().copied().find(|a| a.cpu == want).unwrap_or(arches[0])
}

const LC_SYMTAB: u32 = 0x2;
const LC_DYSYMTAB: u32 = 0xb;
const MAX_LOAD_CMD_BYTES: u64 = 1 << 20;
const NLIST_SIZE: u64 = 16;

/// Reads as `r` with the byte ranges `zero` (start, length) cleared (Go's `zeroedFields`).
pub struct Zeroed<'a> {
    pub r: &'a dyn ReadAt,
    pub zero: Vec<(u64, u64)>,
}

impl ReadAt for Zeroed<'_> {
    fn read_at(&self, off: u64, buf: &mut [u8]) -> Result<usize, String> {
        let n = self.r.read_at(off, buf)?;
        for &(at, len) in &self.zero {
            let (lo, hi) = (at.max(off), (at + len).min(off + n as u64));
            for i in lo..hi {
                buf[(i - off) as usize] = 0;
            }
        }
        Ok(n)
    }
}

/// Go's `boundSymtab`: a reader for the Mach-O at `r`, or `None` when `r` is not Mach-O. When the
/// LC_SYMTAB symbol and string tables together exceed `limit`, the reader presents that command
/// (and LC_DYSYMTAB's indexes) as empty, and the flag is true.
pub fn bound_symtab<'a>(r: &'a dyn ReadAt, limit: i64) -> Option<(Zeroed<'a>, bool)> {
    let plain = || Some((Zeroed { r, zero: Vec::new() }, false));
    let mut hdr = [0u8; 32];
    read_full_at(r, 0, &mut hdr).ok()?;
    let (bo, hdr_size) = macho_byte_order(&hdr[..4])?;
    let (ncmds, size) = (bo.u32(&hdr[16..]), u64::from(bo.u32(&hdr[20..])));
    if size == 0 || size > MAX_LOAD_CMD_BYTES {
        return plain();
    }
    let mut cmds = vec![0u8; size as usize];
    if read_full_at(r, hdr_size, &mut cmds).is_err() {
        return plain();
    }
    let Some(off) = find_load_cmd(&cmds, bo, ncmds, LC_SYMTAB, 24) else { return plain() };
    let (nsyms, strsize) = (u64::from(bo.u32(&cmds[off as usize + 12..])), u64::from(bo.u32(&cmds[off as usize + 20..])));
    if (nsyms * NLIST_SIZE + strsize) as i64 <= limit {
        return plain();
    }
    let mut zero = vec![(hdr_size + off + 12, 4), (hdr_size + off + 20, 4)];
    if let Some(dys) = find_load_cmd(&cmds, bo, ncmds, LC_DYSYMTAB, 32) {
        zero.push((hdr_size + dys + 8, 24));
    }
    Some((Zeroed { r, zero }, true))
}

/// The byte order and header size for a Mach-O magic.
fn macho_byte_order(magic: &[u8]) -> Option<(super::reader::Endian, u64)> {
    use super::reader::Endian;
    for bo in [Endian::Little, Endian::Big] {
        match bo.u32(magic) {
            macho::MAGIC64 => return Some((bo, 32)),
            macho::MAGIC32 => return Some((bo, 28)),
            _ => {}
        }
    }
    None
}

/// The offset in `cmds` of the first load command `want` of at least `min_len` bytes.
fn find_load_cmd(cmds: &[u8], bo: super::reader::Endian, mut ncmds: u32, want: u32, min_len: u64) -> Option<u64> {
    let size = cmds.len() as u64;
    let mut off = 0u64;
    while ncmds > 0 && off + 8 <= size {
        let (cmd, clen) = (bo.u32(&cmds[off as usize..]), u64::from(bo.u32(&cmds[off as usize + 4..])));
        if clen < 8 || off + clen > size {
            break;
        }
        if cmd == want && clen >= min_len {
            return Some(off);
        }
        off += clen;
        ncmds -= 1;
    }
    None
}

/// What the Rust decision needs from a symbol table (Go's `symbolFacts`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct SymbolFacts {
    /// `std::rt::lang_start` is defined.
    pub rust_entry: bool,
    /// Local function symbols survive (not stripped).
    pub locals: bool,
    /// C++ runtime symbols.
    pub cxx: bool,
    /// The table was too large to read; it counts as stripped.
    pub skipped: bool,
}

impl SymbolFacts {
    pub fn add(&mut self, name: &str) {
        if name.contains(RUST_ENTRY_SYMBOL) {
            self.rust_entry = true;
        }
        if cxx_symbol(name) {
            self.cxx = true;
        }
    }
}

/// Go's `cxxSymbol`.
pub fn cxx_symbol(name: &str) -> bool {
    let n = name.trim_start_matches('_');
    matches!(n, "Znwm" | "Znwj" | "cxa_throw" | "cxa_begin_catch" | "gxx_personality_v0") || n.starts_with("ZNSt") || n.starts_with("ZNKSt")
}

/// Go's `machoVerdict`.
pub fn macho_verdict(f: &macho::File, r: &dyn ReadAt, max_scan: i64, syms_skipped: bool) -> BinaryVerdict {
    if f.segment("__BUN").is_some() {
        return BinaryVerdict::new(Runtime::Bun, "Mach-O has a __BUN segment (bun compiled executable)");
    }
    if f.sections.iter().any(|s| s.name == "__go_buildinfo") {
        return BinaryVerdict::new(Runtime::Go, "Mach-O has a __go_buildinfo section");
    }
    let mut budget = max_scan;
    let mut hits = 0;
    for s in &f.sections {
        if !macho_read_only_data(&s.seg, &s.name) || budget <= 0 {
            continue;
        }
        hits += count_markers(&macho::File::section_reader(r, s), &mut budget);
    }
    let libs = f.imported_libraries();
    let mut syms = SymbolFacts::default();
    if hits > 0 {
        if syms_skipped {
            syms.skipped = true;
        } else {
            syms = macho_symbols(f);
        }
    }
    rust_verdict("Mach-O", hits, syms, &libs)
}

fn macho_symbols(f: &macho::File) -> SymbolFacts {
    let mut s = SymbolFacts::default();
    let Some(syms) = &f.symtab else { return s };
    const N_STAB: u8 = 0xe0;
    const N_TYPE: u8 = 0x0e;
    const N_EXT: u8 = 0x01;
    const N_SECT: u8 = 0x0e;
    for sym in syms {
        s.add(&sym.name);
        if sym.typ & N_STAB == 0 && sym.typ & N_TYPE == N_SECT && sym.typ & N_EXT == 0 {
            s.locals = true;
        }
    }
    s
}

/// The size of the symbol and string tables `Symbols` and `DynamicSymbols` would read.
fn elf_symbol_bytes(f: &elf::File) -> u64 {
    let mut total = 0u64;
    for sec in &f.sections {
        if sec.typ != elf::SHT_SYMTAB && sec.typ != elf::SHT_DYNSYM {
            continue;
        }
        total = total.wrapping_add(sec.size.min(1 << 40));
        if let Some(link) = f.sections.get(sec.link as usize) {
            total = total.wrapping_add(link.size.min(1 << 40));
        }
    }
    total
}

fn elf_symbols(f: &elf::File, r: &dyn ReadAt) -> SymbolFacts {
    let mut s = SymbolFacts::default();
    if let Ok(syms) = f.symbols(r, elf::SHT_SYMTAB) {
        for sym in syms {
            s.add(&sym.name);
            // STB_LOCAL and STT_FUNC.
            if sym.info >> 4 == 0 && sym.info & 0xf == 2 {
                s.locals = true;
            }
        }
    }
    if let Ok(syms) = f.symbols(r, elf::SHT_DYNSYM) {
        for sym in syms {
            s.add(&sym.name);
        }
    }
    s
}

/// Go's `rustVerdict`: rust versus native once Rust std markers are counted.
pub fn rust_verdict(format: &str, hits: usize, syms: SymbolFacts, libs: &[String]) -> BinaryVerdict {
    let mut v = rust_verdict_of(format, hits, syms, libs);
    if syms.skipped {
        v.evidence.push_str("; symbol table too large to read, treated as stripped");
    }
    v
}

fn rust_verdict_of(format: &str, hits: usize, syms: SymbolFacts, libs: &[String]) -> BinaryVerdict {
    let cxx = syms.cxx || links_cxx(libs);
    if !cxx && !syms.rust_entry && hits > 0 && links_native_runtime(libs) {
        return BinaryVerdict::new(
            Runtime::Native,
            format!("links Rust std ({hits} rustc markers) with a Swift, Objective-C or Foundation runtime and no std::rt::lang_start"),
        );
    }
    if syms.rust_entry {
        return BinaryVerdict::new(Runtime::Rust, format!("{format} defines std::rt::lang_start (Rust main)"));
    }
    if hits == 0 {
        return BinaryVerdict::new(Runtime::Native, format!("{format} with no Go, Bun or Rust markers"));
    }
    if cxx && syms.locals {
        return BinaryVerdict::new(
            Runtime::Native,
            format!("links Rust std ({hits} rustc markers) with a C++ runtime and no std::rt::lang_start"),
        );
    }
    if cxx {
        return BinaryVerdict::new(
            Runtime::Native,
            format!("links Rust std ({hits} rustc markers) into a stripped binary with a C++ runtime"),
        );
    }
    if syms.locals {
        return BinaryVerdict::new(
            Runtime::Rust,
            format!(
                "{format} with {hits} rustc std markers and no C++ runtime; std::rt::lang_start is absent (link-time optimization can inline it)"
            ),
        );
    }
    BinaryVerdict::new(Runtime::Rust, format!("stripped {format} with {hits} rustc std markers and no C++ runtime"))
}

fn links_native_runtime(libs: &[String]) -> bool {
    libs.iter().any(|l| {
        l.contains("libswiftCore")
            || l.contains("libobjc")
            || l.contains("/Foundation.framework/")
            || l.contains("/CoreFoundation.framework/")
    })
}

fn links_cxx(libs: &[String]) -> bool {
    libs.iter().any(|l| l.contains("libc++") || l.contains("libstdc++"))
}

fn macho_read_only_data(seg: &str, name: &str) -> bool {
    match seg {
        "__TEXT" => name == "__const" || name == "__cstring" || name == "__rodata",
        "__DATA_CONST" => name == "__const",
        _ => false,
    }
}

/// Go's `elfVerdict`.
pub fn elf_verdict(f: &elf::File, r: &dyn ReadAt, max_scan: i64) -> BinaryVerdict {
    if f.section(".go.buildinfo").is_some() {
        return BinaryVerdict::new(Runtime::Go, "ELF has a .go.buildinfo section");
    }
    if f.section(".bun").is_some() {
        return BinaryVerdict::new(Runtime::Bun, "ELF has a .bun section (bun compiled executable)");
    }
    let mut budget = max_scan;
    let mut hits = 0;
    if let Some(s) = f.section(".rodata")
        && s.typ != elf::SHT_NOBITS
    {
        hits = count_markers(&elf::File::section_reader(r, s), &mut budget);
    }
    let libs = f.imported_libraries(r).unwrap_or_default();
    let mut syms = SymbolFacts::default();
    if hits > 0 {
        if max_scan >= 0 && elf_symbol_bytes(f) > max_scan as u64 {
            syms.skipped = true;
        } else {
            syms = elf_symbols(f, r);
        }
    }
    rust_verdict("ELF", hits, syms, &libs)
}

/// Go's `bytes.Count`: non-overlapping occurrences.
fn count(hay: &[u8], needle: &[u8]) -> usize {
    let (mut n, mut i) = (0, 0);
    while i + needle.len() <= hay.len() {
        if &hay[i..i + needle.len()] == needle {
            n += 1;
            i += needle.len();
        } else {
            i += 1;
        }
    }
    n
}

/// Go's `countMarkers`: [`RUST_NEEDLES`] in `r` read from the start, reading at most `*budget`
/// bytes and charging what it read against the budget.
pub fn count_markers(r: &dyn ReadAt, budget: &mut i64) -> usize {
    const CHUNK: usize = 256 << 10;
    let overlap = RUST_NEEDLES.iter().map(|n| n.len() - 1).max().unwrap_or(0);
    let mut buf = vec![0u8; overlap + CHUNK];
    let (mut carry, mut hits, mut pos) = (0usize, 0usize, 0u64);
    while *budget > 0 {
        let want = (CHUNK as i64).min(*budget) as usize;
        let n = r.read_at(pos, &mut buf[carry..carry + want]).unwrap_or(0);
        pos += n as u64;
        *budget -= n as i64;
        let region = &buf[..carry + n];
        for needle in RUST_NEEDLES {
            // Matches wholly inside the carried tail were counted last round.
            hits += count(region, needle) - count(&region[..carry], needle);
        }
        if n < want {
            break;
        }
        // Carry a tail so a match straddling two chunks is still found.
        let len = region.len();
        let next = overlap.min(len);
        buf.copy_within(len - next..len, 0);
        carry = next;
    }
    hits
}
