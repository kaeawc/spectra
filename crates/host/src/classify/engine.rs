//! The classification pipeline (Go's `classify.go`): locate each executable and its bundle,
//! read bundles once, apply the cheap runtime rules, then do the per-executable cold work
//! (reading the binary, running `codesign`) once per file version on a bounded worker pool,
//! cached in memory and, with a [`Store`], on disk.

use std::collections::HashMap;
use std::path::Path;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};

use serde::{Deserialize, Serialize};
use spectra_base::fs::{FileInfo, FileType, Fs};
use spectra_base::fsutil::identity::{FileId, identity};
use spectra_base::gojson;
use spectra_base::hostos::Kind;
use spectra_base::proc::{CancelToken, Runner};

use super::bundle::{BundleInfo, BundleLoc, bundle_name, locate_bundle, read_bundle};
use super::files::{eval_symlinks, mode_type};
use super::gopath::{base, dir, ext, is_abs, join};
use super::role::{RoleInput, UNVERIFIED, decide_role, interpreter_args_script, is_kernel_thread, npm_package};
use super::runtime::{BinaryVerdict, inspect_binary, interpreter_runtime};
use super::signing::{SigningVerdict, read_signing, signing_evidence};
use super::{App, Cancelled, Classification, DEFAULT_MAX_SCAN_BYTES, Input, Options, Runtime, Store, sha256};
use crate::goerr;

/// Prefixes every disk key; bump it whenever [`ExeEntry`] (or anything it contains) changes shape
/// or meaning.
pub const CACHE_VERSION: &str = "classify/v6";

/// Identity of the enclosing bundle's CodeResources: `codesign --verify --strict` checks sealed
/// resources, so a signing verdict goes stale when they change though the executable does not.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
struct SealId {
    size: i64,
    mtime: i64,
    id: FileId,
}

/// One version of an executable file (Go's `exeKey`). Size and mtime alone survive `cp -p`,
/// rsync and tar; the inode and kernel-set ctime do not.
#[derive(Clone, Debug, Default, PartialEq, Eq, Hash)]
struct ExeKey {
    path: String,
    size: i64,
    mtime: i64,
    id: FileId,
    seal: SealId,
}

/// The cached per-executable work (Go's `exeEntry`); its JSON is what the [`Store`] holds.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ExeEntry {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub binary: Option<BinaryVerdict>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub signing: Option<SigningVerdict>,
    /// A part failed for a reason that says nothing durable about the file: it answers this
    /// call as unknown, is never written to disk, and is redone on the next call.
    #[serde(skip)]
    pub retry_bin: bool,
    #[serde(skip)]
    pub retry_sign: bool,
}

/// The per-process state gathered before the expensive phase (Go's `plan`).
#[derive(Clone, Default)]
struct Plan {
    index: usize,
    /// Kernel-reported executable path.
    exe: String,
    /// Where to read it: `exe`, or `/proc/<pid>/exe` on Linux.
    read: String,
    loc: BundleLoc,
    bundle: BundleInfo,
    runtime: Runtime,
    evidence: String,
    key: Option<ExeKey>,
    need_bin: bool,
    need_sign: bool,
}

/// The cold work for one executable version (Go's `job`).
#[derive(Clone)]
struct Job {
    key: ExeKey,
    read: String,
    need_bin: bool,
    need_sign: bool,
}

pub struct Engine {
    os: Kind,
    runner: Arc<dyn Runner>,
    fs: Arc<dyn Fs>,
    store: Option<Arc<dyn Store>>,
    workers: usize,
    max_scan: i64,
    procfs: String,
    arch: String,
    exes: Mutex<HashMap<ExeKey, ExeEntry>>,
    bundles: Mutex<HashMap<String, BundleInfo>>,
}

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(std::sync::PoisonError::into_inner)
}

/// Go's `runtime.GOARCH` for the host.
fn host_arch() -> &'static str {
    match std::env::consts::ARCH {
        "aarch64" => "arm64",
        "x86_64" => "amd64",
        other => other,
    }
}

fn nanos(t: Option<(i64, u32)>) -> i64 {
    t.map_or(0, |(s, n)| s.wrapping_mul(1_000_000_000).wrapping_add(i64::from(n)))
}

fn size_of(fi: &FileInfo) -> i64 {
    i64::try_from(fi.len).unwrap_or(i64::MAX)
}

impl Engine {
    pub fn new(opts: Options) -> Engine {
        Engine {
            os: opts.os.resolve(),
            runner: opts.runner,
            fs: opts.fs,
            store: opts.store,
            workers: if opts.workers == 0 { 8 } else { opts.workers },
            max_scan: if opts.max_scan_bytes <= 0 { DEFAULT_MAX_SCAN_BYTES } else { opts.max_scan_bytes },
            procfs: if opts.proc_fs.is_empty() { "/proc".into() } else { opts.proc_fs },
            arch: if opts.arch.is_empty() { host_arch().into() } else { opts.arch },
            exes: Mutex::new(HashMap::new()),
            bundles: Mutex::new(HashMap::new()),
        }
    }

    /// Go's `Classify`.
    pub fn classify(
        &self,
        inputs: &[Input],
        load_argv: &dyn Fn(usize) -> Vec<String>,
        cancel: &CancelToken,
    ) -> (Vec<Classification>, Option<Cancelled>) {
        let mut plans: Vec<Plan> = inputs.iter().enumerate().map(|(i, input)| self.locate(i, input)).collect();
        let mut err = self.load_bundles(cancel, &mut plans);
        for p in &mut plans {
            self.prepare(&inputs[p.index], p);
        }
        let fill_err = self.fill(cancel, &plans);
        if err.is_none() {
            err = fill_err;
        }
        let out = plans.iter().map(|p| self.finish(&inputs[p.index], p, load_argv)).collect();
        (out, err)
    }

    /// Go's `locate`: the executable and its bundle, without touching the disk. argv[0] is never
    /// used as a path: any process can set it (`exec -a`).
    fn locate(&self, index: usize, input: &Input) -> Plan {
        let mut p = Plan { index, exe: input.executable_path.clone(), read: input.executable_path.clone(), ..Plan::default() };
        if self.os == Kind::Linux && !p.exe.is_empty() && input.pid > 0 {
            p.read = join(&[&self.procfs, &input.pid.to_string(), "exe"]);
        }
        if self.os == Kind::Darwin {
            p.loc = locate_bundle(&p.exe);
        }
        p
    }

    /// Go's `loadBundles`: each owning bundle read once, on the worker pool, so a slow or hostile
    /// bundle cannot stall the listing past cancellation.
    fn load_bundles(&self, cancel: &CancelToken, plans: &mut [Plan]) -> Option<Cancelled> {
        let mut roots: Vec<String> = Vec::new();
        for p in plans.iter() {
            let root = &p.loc.root;
            if root.is_empty() || p.loc.framework_internal || roots.contains(root) {
                continue;
            }
            if !lock(&self.bundles).contains_key(root) {
                roots.push(root.clone());
            }
        }
        let err = self.pool(cancel, roots.len(), &|i| {
            let b = read_bundle(self.fs.as_ref(), &roots[i]);
            lock(&self.bundles).insert(roots[i].clone(), b);
        });
        let bundles = lock(&self.bundles);
        for p in plans.iter_mut() {
            if p.loc.root.is_empty() || p.loc.framework_internal {
                continue;
            }
            p.bundle =
                bundles.get(&p.loc.root).cloned().unwrap_or_else(|| BundleInfo { name: bundle_name(&p.loc.root), ..BundleInfo::default() });
        }
        err
    }

    /// Go's `prepare`: the cheap runtime rules, and the executable keyed for the cold phase.
    fn prepare(&self, input: &Input, p: &mut Plan) {
        (p.runtime, p.evidence) = self.name_runtime(input, p);
        p.need_bin = p.runtime == Runtime::Unset;
        p.need_sign = self.os == Kind::Darwin && !p.exe.is_empty();
        if !p.need_bin && !p.need_sign {
            return;
        }
        let fail = |p: &mut Plan, ev: String| {
            if p.need_bin {
                (p.runtime, p.evidence) = (Runtime::Unknown, ev);
                p.need_bin = false;
            }
            p.need_sign = false;
        };
        if p.exe.is_empty() {
            fail(p, "executable path not visible".into());
            return;
        }
        let fi = match self.fs.metadata(Path::new(&p.read)) {
            Ok(fi) => fi,
            Err(e) => return fail(p, format!("executable unreadable: {}", goerr::path_error("stat", &p.read, &e))),
        };
        if fi.file_type != FileType::File {
            return fail(p, format!("executable is not a regular file ({})", mode_type(&fi)));
        }
        p.key = Some(ExeKey {
            path: p.exe.clone(),
            size: size_of(&fi),
            mtime: nanos(fi.modified),
            id: identity(&fi),
            seal: self.seal_identity(&p.exe),
        });
    }

    /// Go's `sealIdentity`: the CodeResources of the nearest `.app` or `.framework` enclosing
    /// `exe`; zero when there is none.
    fn seal_identity(&self, exe: &str) -> SealId {
        let mut d = dir(exe);
        while d != "/" && d != "." {
            let rel = match ext(&d) {
                ".app" => "Contents/_CodeSignature/CodeResources",
                ".framework" => "Versions/Current/_CodeSignature/CodeResources",
                _ => {
                    d = dir(&d);
                    continue;
                }
            };
            return match self.fs.metadata(Path::new(&join(&[&d, rel]))) {
                Ok(fi) => SealId { size: size_of(&fi), mtime: nanos(fi.modified), id: identity(&fi) },
                Err(_) => SealId::default(),
            };
        }
        SealId::default()
    }

    /// Go's `nameRuntime`: the path-only runtime rules, in order; [`Runtime::Unset`] means the
    /// binary itself must be inspected.
    fn name_runtime(&self, input: &Input, p: &Plan) -> (Runtime, String) {
        if self.os == Kind::Linux && is_kernel_thread(&p.exe, input.pid, input.ppid, &input.command_line) {
            return (Runtime::Native, "kernel thread".into());
        }
        if p.exe.is_empty() {
            let b = input.command.strip_prefix('-').unwrap_or(&input.command);
            if let Some(rt) = interpreter_runtime(b) {
                return (rt, format!("argv[0] {b} names an interpreter{UNVERIFIED}"));
            }
            return (Runtime::Unknown, "executable path not visible".into());
        }
        let b = base(&p.exe);
        if let Some(rt) = interpreter_runtime(&b) {
            return (rt, format!("interpreter executable {b}"));
        }
        if p.loc.app_owned() && p.bundle.electron {
            return (Runtime::Electron, format!("{} contains Electron Framework.framework", base(&p.loc.root)));
        }
        if p.loc.app_owned() && !p.bundle.jvm.is_empty() && p.bundle.is_main_executable(&p.loc.root, &p.exe) {
            return (Runtime::Jvm, format!("app bundles a Java runtime at {}", p.bundle.jvm));
        }
        (Runtime::Unset, String::new())
    }

    /// Go's `pool`: `work(0..n)` on at most `workers` threads, handing out no work once `cancel`
    /// is set, and [`Cancelled`] when it is set at the end.
    pub(super) fn pool(&self, cancel: &CancelToken, n: usize, work: &(dyn Fn(usize) + Sync)) -> Option<Cancelled> {
        let next = AtomicUsize::new(0);
        let worker = || {
            loop {
                // A job must not start after cancellation.
                if cancel.is_cancelled() {
                    break;
                }
                let i = next.fetch_add(1, Ordering::SeqCst);
                if i >= n {
                    break;
                }
                work(i);
            }
        };
        match self.workers.min(n) {
            0 => {}
            1 => worker(),
            w => std::thread::scope(|s| {
                for _ in 0..w {
                    s.spawn(worker);
                }
            }),
        }
        cancel.is_cancelled().then_some(Cancelled)
    }

    /// Go's `fill`: every keyed plan's executable gets the entry parts it needs, from memory,
    /// then disk, then the worker pool.
    fn fill(&self, cancel: &CancelToken, plans: &[Plan]) -> Option<Cancelled> {
        let jobs = self.pending_jobs(plans);
        self.pool(cancel, jobs.len(), &|i| self.run(cancel, &jobs[i]))
    }

    fn pending_jobs(&self, plans: &[Plan]) -> Vec<Job> {
        let mut jobs: Vec<Job> = Vec::new();
        for p in plans {
            let Some(key) = &p.key else { continue };
            match jobs.iter_mut().find(|j| &j.key == key) {
                Some(j) => {
                    j.need_bin |= p.need_bin;
                    j.need_sign |= p.need_sign;
                }
                None => jobs.push(Job { key: key.clone(), read: p.read.clone(), need_bin: p.need_bin, need_sign: p.need_sign }),
            }
        }
        jobs.retain(|j| {
            let e = self.lookup(&j.key);
            j.need_bin && (e.binary.is_none() || e.retry_bin) || j.need_sign && (e.signing.is_none() || e.retry_sign)
        });
        jobs
    }

    /// Go's `lookup`: the cached entry for `k`, promoting a disk hit into memory.
    fn lookup(&self, k: &ExeKey) -> ExeEntry {
        if let Some(e) = lock(&self.exes).get(k) {
            return e.clone();
        }
        let Some(store) = &self.store else { return ExeEntry::default() };
        let Ok(Some(data)) = store.get(&self.disk_key(k)) else { return ExeEntry::default() };
        let Ok(e) = serde_json::from_slice::<ExeEntry>(&data) else { return ExeEntry::default() };
        lock(&self.exes).insert(k.clone(), e.clone());
        e
    }

    fn run(&self, cancel: &CancelToken, j: &Job) {
        let mut e = self.lookup(&j.key);
        if j.need_bin && (e.binary.is_none() || e.retry_bin) {
            let (v, err) = inspect_binary(self.fs.as_ref(), &j.read, self.max_scan, &self.arch);
            (e.binary, e.retry_bin) = (Some(v), err.is_some());
        }
        if j.need_sign && (e.signing.is_none() || e.retry_sign) && !cancel.is_cancelled() {
            let (v, err) = read_signing(self.os, self.runner.as_ref(), cancel, &j.read);
            (e.signing, e.retry_sign) = (Some(v), err.is_some());
        }
        lock(&self.exes).insert(j.key.clone(), e.clone());
        if !e.retry_bin
            && !e.retry_sign
            && let Some(store) = &self.store
            && let Ok(data) = gojson::to_vec(&e)
        {
            let _ = store.put(&self.disk_key(&j.key), &data);
        }
    }

    /// Go's `diskKey`: SHA-256 (Go's `cache.Key`) over the version, OS and key fields.
    fn disk_key(&self, k: &ExeKey) -> Vec<u8> {
        let mut b = Vec::new();
        let mut put = |s: String| {
            b.extend_from_slice(s.as_bytes());
            b.push(0);
        };
        put(CACHE_VERSION.into());
        put(self.os.as_str().into());
        put(k.path.clone());
        for n in [k.size, k.mtime] {
            put(n.to_string());
        }
        put(k.id.dev.to_string());
        put(k.id.ino.to_string());
        put(k.id.ctime.to_string());
        put(k.seal.size.to_string());
        put(k.seal.mtime.to_string());
        put(k.seal.id.dev.to_string());
        put(k.seal.id.ino.to_string());
        put(k.seal.id.ctime.to_string());
        sha256::sum(&b).to_vec()
    }

    fn finish(&self, input: &Input, p: &Plan, load_argv: &dyn Fn(usize) -> Vec<String>) -> Classification {
        let e = p.key.as_ref().and_then(|k| lock(&self.exes).get(k).cloned()).unwrap_or_default();
        let mut r = Classification { runtime: p.runtime, ..Classification::default() };
        let mut runtime_ev = p.evidence.clone();
        let mut go_mod = String::new();
        if p.need_bin {
            match &e.binary {
                Some(b) => (r.runtime, runtime_ev, go_mod) = (b.runtime, b.evidence.clone(), b.go_module.clone()),
                None => (r.runtime, runtime_ev) = (Runtime::Unknown, "classification cancelled before the binary was read".into()),
            }
        }
        if input.executable_deleted {
            runtime_ev.push_str(&format!("; {} was deleted, the running image was read through {}", p.exe, p.read));
        }
        let (mut platform, mut sign_ev) = (false, String::new());
        if p.need_sign
            && let Some(s) = &e.signing
        {
            r.signing = Some(s.signing.clone());
            platform = s.platform;
            sign_ev = signing_evidence(s);
        }
        if !p.loc.root.is_empty() && !p.loc.framework_internal {
            r.app = Some(App { name: p.bundle.name.clone(), bundle_path: p.loc.root.clone(), bundle_id: p.bundle.bundle_id.clone() });
        }
        let script = self.script(input, p.index, r.runtime, load_argv, &mut r.loaded_argv);
        let (role, role_ev) = decide_role(&RoleInput {
            os: self.os,
            pid: input.pid,
            ppid: input.ppid,
            uid: input.uid,
            exe: p.exe.clone(),
            argv0: input.command.clone(),
            cmdline: input.command_line.clone(),
            runtime: r.runtime,
            loc: p.loc.clone(),
            bundle: p.bundle.clone(),
            platform,
            go_module: go_mod,
            script,
        });
        r.role = role;
        r.evidence = vec![format!("role: {role_ev}"), format!("runtime: {runtime_ev}")];
        if !sign_ev.is_empty() {
            r.evidence.push(sign_ev);
        }
        r
    }

    /// Go's `script`: the script an interpreter runs, following symlinks (an npm bin/ link) when
    /// the path alone names no package.
    fn script(
        &self,
        input: &Input,
        index: usize,
        rt: Runtime,
        load_argv: &dyn Fn(usize) -> Vec<String>,
        loaded: &mut Option<Vec<String>>,
    ) -> String {
        if !matches!(rt, Runtime::Node | Runtime::Bun | Runtime::Deno | Runtime::Python) {
            return String::new();
        }
        let s = interpreter_args_script(&script_args(input, index, load_argv, loaded), rt);
        if s.is_empty() || !npm_package(&s).is_empty() || !is_abs(&s) {
            return s;
        }
        eval_symlinks(self.fs.as_ref(), &s).unwrap_or(s)
    }
}

/// Go's `scriptArgs`: argv when known, otherwise the command line split on whitespace.
fn script_args(input: &Input, index: usize, load_argv: &dyn Fn(usize) -> Vec<String>, loaded: &mut Option<Vec<String>>) -> Vec<String> {
    if !input.argv.is_empty() {
        return input.argv.clone();
    }
    if input.load_argv {
        let argv = load_argv(index);
        *loaded = Some(argv.clone());
        if !argv.is_empty() {
            return argv;
        }
    }
    input.command_line.split_whitespace().map(String::from).collect()
}
