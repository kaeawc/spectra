//! Which code tools this Mac has, for the code view: debuggers, disassemblers, decompilers and
//! formatters, found by looking on `PATH`, through `xcrun -f` and in the folders they're known to
//! install into. Nothing is installed. The answer is kept for the app's lifetime and looked for
//! again when a view finds nothing to run.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::time::Duration;

/// A tool the code view can run.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum Tool {
    Lldb,
    Objdump,
    Otool,
    SwiftDemangle,
    CxxFilt,
    Ghidra,
    R2,
    RetDec,
    Javap,
    Java,
    /// CFR as a command.
    Cfr,
    /// CFR as a jar, run with `java -jar`.
    CfrJar,
    Jadx,
    Procyon,
    ProcyonJar,
    /// Fernflower as a jar (an IDE's bundled one), run with `java -cp`.
    FernflowerJar,
    Python,
    Pycdc,
    Prettier,
    JsBeautify,
}

impl Tool {
    pub const ALL: [Tool; 20] = [
        Tool::Lldb,
        Tool::Objdump,
        Tool::Otool,
        Tool::SwiftDemangle,
        Tool::CxxFilt,
        Tool::Ghidra,
        Tool::R2,
        Tool::RetDec,
        Tool::Javap,
        Tool::Java,
        Tool::Cfr,
        Tool::CfrJar,
        Tool::Jadx,
        Tool::Procyon,
        Tool::ProcyonJar,
        Tool::FernflowerJar,
        Tool::Python,
        Tool::Pycdc,
        Tool::Prettier,
        Tool::JsBeautify,
    ];

    /// What the page calls it.
    pub fn label(self) -> &'static str {
        match self {
            Tool::Lldb => "lldb",
            Tool::Objdump => "llvm-objdump",
            Tool::Otool => "otool",
            Tool::SwiftDemangle => "swift-demangle",
            Tool::CxxFilt => "c++filt",
            Tool::Ghidra => "Ghidra",
            Tool::R2 => "radare2",
            Tool::RetDec => "RetDec",
            Tool::Javap => "javap",
            Tool::Java => "java",
            Tool::Cfr | Tool::CfrJar => "CFR",
            Tool::Jadx => "jadx",
            Tool::Procyon | Tool::ProcyonJar => "Procyon",
            Tool::FernflowerJar => "Fernflower",
            Tool::Python => "python3",
            Tool::Pycdc => "pycdc",
            Tool::Prettier => "prettier",
            Tool::JsBeautify => "js-beautify",
        }
    }

    /// The commands it goes by, looked for on `PATH` and in the known folders.
    fn commands(self) -> &'static [&'static str] {
        match self {
            Tool::Lldb => &["lldb"],
            Tool::Objdump => &["llvm-objdump"],
            Tool::Otool => &["otool"],
            Tool::SwiftDemangle => &["swift-demangle"],
            Tool::CxxFilt => &["c++filt"],
            Tool::Ghidra => &["analyzeHeadless"],
            Tool::R2 => &["r2", "radare2"],
            Tool::RetDec => &["retdec-decompiler"],
            Tool::Javap => &["javap"],
            Tool::Java => &["java"],
            Tool::Cfr => &["cfr", "cfr-decompiler"],
            Tool::Jadx => &["jadx"],
            Tool::Procyon => &["procyon", "procyon-decompiler"],
            Tool::Python => &["python3"],
            Tool::Pycdc => &["pycdc"],
            Tool::Prettier => &["prettier"],
            Tool::JsBeautify => &["js-beautify"],
            Tool::CfrJar | Tool::ProcyonJar | Tool::FernflowerJar => &[],
        }
    }

    /// Whether Xcode's toolchain may have it (`xcrun -f`).
    fn xcrun(self) -> bool {
        matches!(
            self,
            Tool::Lldb
                | Tool::Objdump
                | Tool::Otool
                | Tool::SwiftDemangle
                | Tool::CxxFilt
                | Tool::Python
        )
    }
}

/// What was found, by tool.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Tools {
    found: BTreeMap<Tool, PathBuf>,
}

impl Tools {
    pub fn get(&self, t: Tool) -> Option<&Path> {
        self.found.get(&t).map(PathBuf::as_path)
    }

    pub fn has(&self, t: Tool) -> bool {
        self.found.contains_key(&t)
    }

    /// The first of `ts` that was found.
    pub fn first(&self, ts: &[Tool]) -> Option<(Tool, &Path)> {
        ts.iter().find_map(|&t| self.get(t).map(|p| (t, p)))
    }

    /// Set tools, for tests.
    #[cfg(test)]
    pub fn with(pairs: &[(Tool, &str)]) -> Tools {
        Tools {
            found: pairs.iter().map(|(t, p)| (*t, PathBuf::from(p))).collect(),
        }
    }
}

/// The filesystem and environment detection reads. Tests swap in a fake.
pub trait Probe: Send + Sync {
    /// `PATH`'s folders.
    fn path_dirs(&self) -> Vec<PathBuf>;
    fn home(&self) -> Option<PathBuf>;
    fn var(&self, name: &str) -> Option<String>;
    /// An executable or readable file.
    fn is_file(&self, p: &Path) -> bool;
    /// A folder's entries, sorted; empty when it can't be read.
    fn list(&self, dir: &Path) -> Vec<PathBuf>;
    /// `xcrun -f <name>`.
    fn xcrun(&self, name: &str) -> Option<PathBuf>;
    /// Whether the jar (a zip) at `jar` holds `entry`, such as `com/acme/Ledger.class`.
    fn jar_has(&self, jar: &Path, entry: &str) -> bool;
    /// The path with its links and `..` resolved; none when it isn't there.
    fn canonical(&self, p: &Path) -> Option<PathBuf>;
    /// Who owns `p` and its permission bits, not following a link.
    fn owner_mode(&self, p: &Path) -> Option<(u32, u32)>;
    /// The user this app runs as.
    fn uid(&self) -> u32;
    /// The JDK `/usr/libexec/java_home` names.
    fn java_home(&self) -> Option<PathBuf>;
}

unsafe extern "C" {
    safe fn getuid() -> u32;
}

/// The real filesystem.
pub struct SystemProbe;

impl Probe for SystemProbe {
    fn path_dirs(&self) -> Vec<PathBuf> {
        std::env::var_os("PATH")
            .map(|p| std::env::split_paths(&p).collect())
            .unwrap_or_default()
    }

    fn home(&self) -> Option<PathBuf> {
        std::env::var_os("HOME").map(PathBuf::from)
    }

    fn var(&self, name: &str) -> Option<String> {
        std::env::var(name).ok().filter(|v| !v.is_empty())
    }

    fn is_file(&self, p: &Path) -> bool {
        p.is_file()
    }

    fn list(&self, dir: &Path) -> Vec<PathBuf> {
        let mut v: Vec<PathBuf> = std::fs::read_dir(dir)
            .map(|r| r.filter_map(|e| e.ok().map(|e| e.path())).collect())
            .unwrap_or_default();
        v.sort();
        v
    }

    fn xcrun(&self, name: &str) -> Option<PathBuf> {
        let mut cmd = std::process::Command::new("/usr/bin/xcrun");
        cmd.args(["-f", name]);
        let out = crate::run::run(cmd, Duration::from_secs(5), 64 * 1024).ok()?;
        if !out.status.success() {
            return None;
        }
        let p = PathBuf::from(String::from_utf8_lossy(&out.stdout).trim());
        p.is_file().then_some(p)
    }

    fn jar_has(&self, jar: &Path, entry: &str) -> bool {
        zip_names(jar).is_some_and(|names| names.iter().any(|n| n == entry))
    }

    fn canonical(&self, p: &Path) -> Option<PathBuf> {
        std::fs::canonicalize(p).ok()
    }

    fn owner_mode(&self, p: &Path) -> Option<(u32, u32)> {
        use std::os::unix::fs::MetadataExt;
        let m = std::fs::symlink_metadata(p).ok()?;
        Some((m.uid(), m.mode()))
    }

    fn uid(&self) -> u32 {
        getuid()
    }

    fn java_home(&self) -> Option<PathBuf> {
        let cmd = std::process::Command::new("/usr/libexec/java_home");
        let out = crate::run::run(cmd, Duration::from_secs(5), 64 * 1024).ok()?;
        if !out.status.success() {
            return None;
        }
        let p = PathBuf::from(String::from_utf8_lossy(&out.stdout).trim());
        p.is_absolute().then_some(p)
    }
}

/// `O_NONBLOCK`, so opening something that turned into a pipe since it was looked at can't wait.
#[cfg(target_os = "macos")]
const O_NONBLOCK: i32 = 0x0004;
#[cfg(not(target_os = "macos"))]
const O_NONBLOCK: i32 = 0o4000;

/// The entry names in a zip's central directory, reading only its tail. Nothing for anything but
/// a regular file (a pipe or device would never end), or a file that isn't a zip, is ZIP64, or is
/// over 1 GB.
pub fn zip_names(path: &Path) -> Option<Vec<String>> {
    use std::io::{Read, Seek, SeekFrom};
    use std::os::unix::fs::OpenOptionsExt;
    if !std::fs::metadata(path).ok()?.is_file() {
        return None;
    }
    let mut f = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(O_NONBLOCK)
        .open(path)
        .ok()?;
    let meta = f.metadata().ok()?;
    if !meta.is_file() {
        return None;
    }
    let len = meta.len();
    if !(22..=1 << 30).contains(&len) {
        return None;
    }
    // The end record sits in the last 22 bytes plus a comment of up to 64 KiB.
    let tail = len.min(22 + 65_535);
    f.seek(SeekFrom::Start(len - tail)).ok()?;
    let mut buf = vec![0u8; tail as usize];
    f.read_exact(&mut buf).ok()?;
    let at = (0..=buf.len() - 22)
        .rev()
        .find(|&i| buf[i..i + 4] == [0x50, 0x4b, 0x05, 0x06])?;
    let u16_at = |b: &[u8], i: usize| u16::from_le_bytes([b[i], b[i + 1]]) as usize;
    let u32_at = |b: &[u8], i: usize| u32::from_le_bytes([b[i], b[i + 1], b[i + 2], b[i + 3]]);
    let count = u16_at(&buf, at + 10);
    let size = u32_at(&buf, at + 12) as u64;
    let offset = u32_at(&buf, at + 16) as u64;
    if size > 64 << 20 || offset + size > len {
        return None;
    }
    let mut cd = vec![0u8; size as usize];
    f.seek(SeekFrom::Start(offset)).ok()?;
    f.read_exact(&mut cd).ok()?;
    let mut names = Vec::with_capacity(count);
    let mut i = 0;
    while i + 46 <= cd.len() && cd[i..i + 4] == [0x50, 0x4b, 0x01, 0x02] {
        let n = u16_at(&cd, i + 28);
        let extra = u16_at(&cd, i + 30);
        let comment = u16_at(&cd, i + 32);
        let name = cd.get(i + 46..i + 46 + n)?;
        names.push(String::from_utf8_lossy(name).into_owned());
        i += 46 + n + extra + comment;
    }
    Some(names)
}

/// Folders tools install into that a Finder-launched app's `PATH` leaves out.
fn known_dirs(p: &dyn Probe) -> Vec<PathBuf> {
    let mut v: Vec<PathBuf> = ["/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"]
        .iter()
        .map(PathBuf::from)
        .collect();
    if let Some(h) = p.home() {
        for d in [".local/bin", "bin", "retdec/bin"] {
            v.push(h.join(d));
        }
    }
    if let Some(j) = p.var("JAVA_HOME") {
        v.push(PathBuf::from(j).join("bin"));
    }
    v.push(PathBuf::from("/usr/bin"));
    v
}

/// The entries of `dir` whose names start with `prefix`, sorted.
fn matching(p: &dyn Probe, dir: &Path, prefix: &str) -> Vec<PathBuf> {
    p.list(dir)
        .into_iter()
        .filter(|e| {
            e.file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| n.starts_with(prefix))
        })
        .collect()
}

/// The newest (last by name) jar under `root/<version>/` whose name starts with `prefix`.
fn versioned_jar(p: &dyn Probe, root: &Path, prefix: &str) -> Option<PathBuf> {
    p.list(root)
        .into_iter()
        .rev()
        .flat_map(|v| matching(p, &v, prefix))
        .find(|j| j.extension().is_some_and(|e| e == "jar") && p.is_file(j))
}

/// The tools' jars and folders outside `PATH`.
fn installed(p: &dyn Probe, t: Tool) -> Option<PathBuf> {
    let home = p.home();
    let m2 = home.as_ref().map(|h| h.join(".m2/repository"));
    let apps = {
        let mut a = vec![PathBuf::from("/Applications")];
        if let Some(h) = &home {
            a.push(h.join("Applications"));
        }
        a
    };
    match t {
        Tool::CfrJar => {
            let mut roots = vec![
                PathBuf::from("/opt/homebrew/opt/cfr-decompiler/libexec"),
                PathBuf::from("/usr/local/opt/cfr-decompiler/libexec"),
            ];
            roots.retain(|r| !p.list(r).is_empty());
            roots
                .iter()
                .flat_map(|r| matching(p, r, "cfr"))
                .find(|j| j.extension().is_some_and(|e| e == "jar"))
                .or_else(|| versioned_jar(p, &m2?.join("org/benf/cfr"), "cfr-"))
        }
        Tool::ProcyonJar => versioned_jar(
            p,
            &m2?.join("org/bitbucket/mstrobel/procyon-decompiler"),
            "procyon-decompiler-",
        ),
        Tool::FernflowerJar => apps
            .iter()
            .flat_map(|a| {
                let mut v = matching(p, a, "IntelliJ IDEA");
                v.extend(matching(p, a, "Android Studio"));
                v
            })
            .map(|app| app.join("Contents/plugins/java-decompiler/lib/java-decompiler.jar"))
            .find(|j| p.is_file(j)),
        Tool::Ghidra => {
            let mut roots: Vec<PathBuf> = [
                "/opt/homebrew/Caskroom/ghidra",
                "/usr/local/Caskroom/ghidra",
            ]
            .iter()
            .flat_map(|c| p.list(Path::new(c)))
            .collect();
            for a in &apps {
                roots.push(a.clone());
            }
            if let Some(h) = &home {
                roots.push(h.clone());
            }
            roots
                .iter()
                .flat_map(|r| matching(p, r, "ghidra"))
                .map(|g| g.join("support/analyzeHeadless"))
                .find(|s| p.is_file(s))
        }
        _ => None,
    }
}

/// Looks for every tool.
pub fn detect(p: &dyn Probe) -> Tools {
    let mut dirs = p.path_dirs();
    for d in known_dirs(p) {
        if !dirs.contains(&d) {
            dirs.push(d);
        }
    }
    let mut found = BTreeMap::new();
    for t in Tool::ALL {
        let on_path = t.commands().iter().find_map(|c| {
            dirs.iter()
                .map(|d| d.join(c))
                .find(|candidate| p.is_file(candidate))
        });
        let hit = on_path
            .or_else(|| {
                t.xcrun()
                    .then(|| t.commands().iter().find_map(|c| p.xcrun(c)))
                    .flatten()
            })
            .or_else(|| installed(p, t));
        if let Some(path) = hit {
            found.insert(t, path);
        }
    }
    // A jar needs a java to run it.
    if !found.contains_key(&Tool::Java) {
        for t in [Tool::CfrJar, Tool::ProcyonJar, Tool::FernflowerJar] {
            found.remove(&t);
        }
    }
    Tools { found }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::collections::BTreeSet;
    use std::sync::Mutex;

    /// A made-up filesystem: these files exist, folders list what's under them, and `xcrun`
    /// knows these tools.
    #[derive(Default)]
    pub struct FakeProbe {
        pub path: Vec<PathBuf>,
        pub files: BTreeSet<PathBuf>,
        pub xcrun: Vec<(String, PathBuf)>,
        pub home: Option<PathBuf>,
        pub asked: Mutex<Vec<String>>,
        /// Links: a path under the key is really under the value.
        pub links: Vec<(PathBuf, PathBuf)>,
        /// Owner and mode by path; anything else is root's, 0o755.
        pub modes: Vec<(PathBuf, (u32, u32))>,
        pub java_home: Option<PathBuf>,
        /// Calls to `jar_has`.
        pub jars_read: Mutex<usize>,
    }

    /// The user the fake runs as.
    pub const UID: u32 = 501;

    impl FakeProbe {
        pub fn new(path: &[&str], files: &[&str]) -> FakeProbe {
            FakeProbe {
                path: path.iter().map(PathBuf::from).collect(),
                files: files.iter().map(PathBuf::from).collect(),
                home: Some(PathBuf::from("/Users/dev")),
                ..FakeProbe::default()
            }
        }
    }

    impl Probe for FakeProbe {
        fn path_dirs(&self) -> Vec<PathBuf> {
            self.path.clone()
        }
        fn home(&self) -> Option<PathBuf> {
            self.home.clone()
        }
        fn var(&self, _: &str) -> Option<String> {
            None
        }
        fn is_file(&self, p: &Path) -> bool {
            self.files.contains(p)
        }
        fn list(&self, dir: &Path) -> Vec<PathBuf> {
            let mut v: Vec<PathBuf> = self
                .files
                .iter()
                .filter_map(|f| {
                    let rest = f.strip_prefix(dir).ok()?;
                    let first = rest.components().next()?;
                    Some(dir.join(first))
                })
                .collect();
            v.sort();
            v.dedup();
            v
        }
        fn xcrun(&self, name: &str) -> Option<PathBuf> {
            self.asked.lock().unwrap().push(name.to_owned());
            self.xcrun
                .iter()
                .find(|(n, _)| n == name)
                .map(|(_, p)| p.clone())
        }
        fn jar_has(&self, jar: &Path, entry: &str) -> bool {
            *self.jars_read.lock().unwrap() += 1;
            self.files
                .contains(&PathBuf::from(format!("{}!{entry}", jar.display())))
        }
        fn canonical(&self, p: &Path) -> Option<PathBuf> {
            use std::path::Component;
            let mut out = PathBuf::new();
            for c in p.components() {
                match c {
                    Component::ParentDir => {
                        out.pop();
                    }
                    Component::CurDir => {}
                    c => out.push(c),
                }
                if let Some((_, to)) = self.links.iter().find(|(from, _)| *from == out) {
                    out = to.clone();
                }
            }
            Some(out)
        }
        fn owner_mode(&self, p: &Path) -> Option<(u32, u32)> {
            Some(
                self.modes
                    .iter()
                    .find(|(q, _)| q == p)
                    .map_or((0, 0o755), |(_, m)| *m),
            )
        }
        fn uid(&self) -> u32 {
            UID
        }
        fn java_home(&self) -> Option<PathBuf> {
            self.java_home.clone()
        }
    }

    #[test]
    fn a_jar_that_is_a_pipe_is_not_read() {
        let dir = std::env::temp_dir().join(format!(
            "spectra-desktop-zip-fifo-test-{}",
            std::process::id()
        ));
        std::fs::create_dir_all(&dir).unwrap();
        let fifo = dir.join("app.jar");
        let made = std::process::Command::new("/usr/bin/mkfifo")
            .arg(&fifo)
            .status()
            .unwrap();
        assert!(made.success());
        let (tx, rx) = std::sync::mpsc::channel();
        let f = fifo.clone();
        std::thread::spawn(move || {
            let _ = tx.send(zip_names(&f));
        });
        let got = rx.recv_timeout(Duration::from_secs(3));
        let _ = std::fs::remove_file(&fifo);
        let _ = std::fs::remove_dir(&dir);
        assert_eq!(
            got,
            Ok(None),
            "a pipe is no zip, and reading it doesn't wait"
        );
    }

    /// A stored (uncompressed) zip with these entries, empty, in the shape `jar` writes.
    fn zip_of(names: &[&str]) -> Vec<u8> {
        let mut out = Vec::new();
        let mut cd = Vec::new();
        for n in names {
            let off = out.len() as u32;
            out.extend_from_slice(&[0x50, 0x4b, 0x03, 0x04]);
            out.extend_from_slice(&[0u8; 22]);
            out.extend_from_slice(&(n.len() as u16).to_le_bytes());
            out.extend_from_slice(&0u16.to_le_bytes());
            out.extend_from_slice(n.as_bytes());
            cd.extend_from_slice(&[0x50, 0x4b, 0x01, 0x02]);
            cd.extend_from_slice(&[0u8; 24]);
            cd.extend_from_slice(&(n.len() as u16).to_le_bytes());
            cd.extend_from_slice(&[0u8; 12]);
            cd.extend_from_slice(&off.to_le_bytes());
            cd.extend_from_slice(n.as_bytes());
        }
        let cd_off = out.len() as u32;
        out.extend_from_slice(&cd);
        out.extend_from_slice(&[0x50, 0x4b, 0x05, 0x06, 0, 0, 0, 0]);
        out.extend_from_slice(&(names.len() as u16).to_le_bytes());
        out.extend_from_slice(&(names.len() as u16).to_le_bytes());
        out.extend_from_slice(&(cd.len() as u32).to_le_bytes());
        out.extend_from_slice(&cd_off.to_le_bytes());
        out.extend_from_slice(&0u16.to_le_bytes());
        out
    }

    #[test]
    fn a_jars_entries_are_read_from_its_central_directory() {
        let dir =
            std::env::temp_dir().join(format!("spectra-desktop-zip-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let jar = dir.join("ledger.jar");
        std::fs::write(
            &jar,
            zip_of(&[
                "META-INF/MANIFEST.MF",
                "com/acme/Ledger.class",
                "com/acme/Ledger$Entry.class",
            ]),
        )
        .unwrap();
        let not_zip = dir.join("notes.txt");
        std::fs::write(&not_zip, "not a zip at all, just some words that run on").unwrap();
        let names = zip_names(&jar);
        assert!(SystemProbe.jar_has(&jar, "com/acme/Ledger$Entry.class"));
        assert!(!SystemProbe.jar_has(&jar, "com/acme/Other.class"));
        assert_eq!(zip_names(&not_zip), None);
        let _ = std::fs::remove_file(&jar);
        let _ = std::fs::remove_file(&not_zip);
        let _ = std::fs::remove_dir(&dir);
        assert_eq!(names.map(|n| n.len()), Some(3));
    }

    #[test]
    fn path_wins_then_xcrun_then_known_folders() {
        let mut p = FakeProbe::new(
            &["/work/bin", "/usr/bin"],
            &[
                "/work/bin/lldb",
                "/usr/bin/otool",
                "/usr/bin/java",
                "/usr/bin/javap",
                "/opt/homebrew/bin/r2",
                "/opt/homebrew/bin/jadx",
            ],
        );
        p.xcrun = vec![(
            "swift-demangle".into(),
            PathBuf::from("/Xcode/usr/bin/swift-demangle"),
        )];
        let t = detect(&p);
        assert_eq!(t.get(Tool::Lldb), Some(Path::new("/work/bin/lldb")));
        assert_eq!(t.get(Tool::Otool), Some(Path::new("/usr/bin/otool")));
        assert_eq!(
            t.get(Tool::SwiftDemangle),
            Some(Path::new("/Xcode/usr/bin/swift-demangle"))
        );
        // Homebrew's folder is looked in even when PATH leaves it out.
        assert_eq!(t.get(Tool::R2), Some(Path::new("/opt/homebrew/bin/r2")));
        assert_eq!(t.get(Tool::Jadx), Some(Path::new("/opt/homebrew/bin/jadx")));
        assert!(!t.has(Tool::Ghidra));
        assert!(!t.has(Tool::RetDec));
        // xcrun is asked only for Xcode's tools, never for a decompiler.
        let asked = p.asked.lock().unwrap().clone();
        assert!(asked.contains(&"swift-demangle".to_owned()));
        assert!(!asked.iter().any(|a| a == "jadx" || a == "cfr" || a == "r2"));
    }

    #[test]
    fn jars_are_found_in_maven_ide_and_cask_folders() {
        let p = FakeProbe::new(
            &["/usr/bin"],
            &[
                "/usr/bin/java",
                "/Users/dev/.m2/repository/org/benf/cfr/0.150/cfr-0.150.jar",
                "/Users/dev/.m2/repository/org/benf/cfr/0.152/cfr-0.152.jar",
                "/Applications/IntelliJ IDEA CE.app/Contents/plugins/java-decompiler/lib/java-decompiler.jar",
                "/opt/homebrew/Caskroom/ghidra/11.2/ghidra_11.2_PUBLIC/support/analyzeHeadless",
            ],
        );
        let t = detect(&p);
        assert_eq!(
            t.get(Tool::CfrJar),
            Some(Path::new(
                "/Users/dev/.m2/repository/org/benf/cfr/0.152/cfr-0.152.jar"
            ))
        );
        assert_eq!(
            t.get(Tool::FernflowerJar),
            Some(Path::new(
                "/Applications/IntelliJ IDEA CE.app/Contents/plugins/java-decompiler/lib/java-decompiler.jar"
            ))
        );
        assert_eq!(
            t.get(Tool::Ghidra),
            Some(Path::new(
                "/opt/homebrew/Caskroom/ghidra/11.2/ghidra_11.2_PUBLIC/support/analyzeHeadless"
            ))
        );
    }

    #[test]
    fn a_jar_without_a_java_to_run_it_is_left_out() {
        let p = FakeProbe::new(
            &[],
            &["/Users/dev/.m2/repository/org/benf/cfr/0.152/cfr-0.152.jar"],
        );
        let t = detect(&p);
        assert!(!t.has(Tool::CfrJar));
        assert_eq!(t, Tools::default());
    }
}
