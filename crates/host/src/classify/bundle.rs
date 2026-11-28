//! Where an executable sits in its `.app`, and what the classifier needs from that bundle (Go's
//! `bundle.go`).

use spectra_base::fs::Fs;
use spectra_base::plist::{self, Value};

use super::files::read_file;
use super::gopath::{base, dir, is_abs, join};

/// Where an executable sits relative to its outermost `.app` (Go's `bundleLoc`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BundleLoc {
    /// Outermost `.app` path; empty when the executable is not in one.
    pub root: String,
    /// A `.framework` precedes the `.app` in the path (Homebrew's Python.framework/.../Python.app).
    pub framework_internal: bool,
    /// Under `<app>/Contents/Developer/` (Xcode's toolchain).
    pub developer: bool,
}

impl BundleLoc {
    /// Whether the bundle makes the process an app or app helper.
    pub fn app_owned(&self) -> bool {
        !self.root.is_empty() && !self.framework_internal && !self.developer
    }
}

/// Go's `locateBundle`.
pub fn locate_bundle(exe: &str) -> BundleLoc {
    if !is_abs(exe) {
        return BundleLoc::default();
    }
    let parts: Vec<&str> = exe.split('/').collect();
    let mut loc = BundleLoc::default();
    for (i, part) in parts[..parts.len() - 1].iter().enumerate() {
        if part.ends_with(".framework") {
            loc.framework_internal = true;
        }
        if part.ends_with(".app") {
            loc.root = parts[..=i].join("/");
            loc.developer = exe.starts_with(&format!("{}/Contents/Developer/", loc.root));
            return loc;
        }
    }
    BundleLoc::default()
}

/// What the classifier needs from one `.app`, read once per run (Go's `bundleInfo`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BundleInfo {
    pub name: String,
    pub bundle_id: String,
    /// Absolute path of CFBundleExecutable; empty when unknown.
    pub main_exe: String,
    pub electron: bool,
    /// The bundled Java runtime directory (JetBrains' Contents/jbr, jpackage's Contents/runtime).
    pub jvm: String,
}

/// Caps one Info.plist read; real ones are a few KiB.
pub const MAX_PLIST_BYTES: u64 = 1 << 20;

/// The name Go gives a bundle it could not read: the `.app` base name.
pub fn bundle_name(root: &str) -> String {
    let b = base(root);
    b.strip_suffix(".app").unwrap_or(&b).to_string()
}

/// Go's `readBundle`.
pub fn read_bundle(fs: &dyn Fs, root: &str) -> BundleInfo {
    let mut info = BundleInfo { name: bundle_name(root), ..BundleInfo::default() };
    let contents = join(&[root, "Contents"]);
    if let Ok(data) = read_file(fs, &join(&[&contents, "Info.plist"]), MAX_PLIST_BYTES)
        && let Ok(v) = plist::decode(&data)
    {
        info.bundle_id = plist_string(&v, "CFBundleIdentifier");
        let exe = plist_string(&v, "CFBundleExecutable");
        if !exe.is_empty() && !exe.contains('/') {
            info.main_exe = join(&[&contents, "MacOS", &exe]);
        }
    }
    if fs.metadata(join(&[&contents, "Frameworks", "Electron Framework.framework"]).as_ref()).is_ok() {
        info.electron = true;
    }
    for rel in ["jbr/Contents/Home", "runtime/Contents/Home"] {
        let d = join(&[&contents, rel]);
        if fs.metadata(join(&[&d, "bin", "java"]).as_ref()).is_ok() {
            info.jvm = d;
            break;
        }
    }
    info
}

fn plist_string(v: &Value, key: &str) -> String {
    match v.lookup(key) {
        Some(Value::String(s)) => s.clone(),
        _ => String::new(),
    }
}

impl BundleInfo {
    /// Whether `exe` is the bundle's CFBundleExecutable. Without a readable Info.plist any
    /// executable directly in Contents/MacOS counts as main.
    pub fn is_main_executable(&self, root: &str, exe: &str) -> bool {
        if !self.main_exe.is_empty() {
            return exe == self.main_exe;
        }
        dir(exe) == join(&[root, "Contents", "MacOS"])
    }
}
