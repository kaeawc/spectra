//! The Toolchains page's data: what developer tooling is installed on this Mac, from
//! `spectra toolchain --json` (Java installs, Node, Python, Go, Ruby and Rust, build tools and
//! Homebrew).
//!
//! It is read once, when the page is first opened, and again on a press of Refresh; never on a
//! timer. `toolchain` is not a daemon method, so the run seam runs the CLI, on a thread of its
//! own with a deadline and the seam's output cap (the real output is tens of kilobytes).
//! The JSON is read tolerantly: a missing field is an empty one, a section this app doesn't know
//! is shown as a plain list when it looks like one, and anything else is ignored.

use std::collections::{BTreeMap, HashSet};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use rune::TextEdit;
use serde_json::Value;

use crate::app::App;
use crate::storage::{Ran, Runner, SpectraBin};

/// Spectra asks Homebrew and each version manager in turn; a real run took about 8 seconds.
pub const DEADLINE: Duration = Duration::from_secs(120);
/// Rows drawn per section before "narrow it with the search".
pub const SHOWN: usize = 200;

pub fn args() -> Vec<String> {
    ["toolchain", "--json"].map(String::from).to_vec()
}

/// One installed thing: a JDK, a runtime version, a Homebrew formula.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Row {
    pub name: String,
    pub version: String,
    /// Where it came from: `system`, `brew`, `nvm`, `asdf`, `core`, `tap`.
    pub source: String,
    pub path: String,
    /// Plain flags: `JAVA_HOME`, `active`, `default`, `pinned`, `deprecated`.
    pub marks: Vec<String>,
}

impl Row {
    /// Whether the lower-cased search words `q` all appear in the name, version or source.
    pub fn matches(&self, q: &str) -> bool {
        let hay = format!("{} {} {}", self.name, self.version, self.source).to_lowercase();
        q.split_whitespace().all(|w| hay.contains(w))
    }
}

/// A group of rows: one kind of toolchain.
#[derive(Clone, Debug, PartialEq)]
pub struct Section {
    pub id: String,
    pub title: String,
    pub rows: Vec<Row>,
}

impl Section {
    /// The rows that match the lower-cased search `q`; all of them when it is empty.
    pub fn matching(&self, q: &str) -> Vec<&Row> {
        self.rows
            .iter()
            .filter(|r| q.is_empty() || r.matches(q))
            .collect()
    }

    /// Opens on its own unless it is Homebrew's long lists.
    pub fn default_open(&self) -> bool {
        !self.id.starts_with("brew_")
    }
}

/// The whole inventory.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Inventory {
    /// In the order the page shows them: Java and the language runtimes first, Homebrew last.
    pub sections: Vec<Section>,
    java_home: String,
    /// The major version and path of every JDK found.
    jdks: Vec<(u64, String)>,
    /// Whether the JAVA_HOME folder exists; `None` until checked.
    java_home_exists: Option<bool>,
    deprecated: usize,
}

impl Inventory {
    pub fn total(&self) -> usize {
        self.sections.iter().map(|s| s.rows.len()).sum()
    }

    /// Looks for the JAVA_HOME folder with `exists` (the disk, off the UI thread; a fake in tests).
    pub fn check_java_home(&mut self, exists: impl Fn(&str) -> bool) {
        self.java_home_exists = (!self.java_home.is_empty()).then(|| exists(&self.java_home));
    }

    /// Facts worth a look, in plain words.
    pub fn flags(&self) -> Vec<String> {
        let mut out = Vec::new();
        let mut by_major: BTreeMap<u64, usize> = BTreeMap::new();
        for (major, _) in self.jdks.iter().filter(|(m, _)| *m > 0) {
            *by_major.entry(*major).or_default() += 1;
        }
        for (major, n) in by_major.into_iter().filter(|(_, n)| *n > 1) {
            out.push(format!(
                "Java {major} is installed {n} times. Which one runs depends on JAVA_HOME and the order of PATH."
            ));
        }
        if self.java_home_exists == Some(false) {
            out.push(format!(
                "JAVA_HOME points to {}, which isn't there. Java tools that follow it will fail to start. (The JAVA_HOME this app was started with.)",
                self.java_home
            ));
        }
        if self.deprecated > 0 {
            out.push(format!(
                "{} installed Homebrew formula{} deprecated: Homebrew no longer maintains {}.",
                self.deprecated,
                if self.deprecated == 1 { " is" } else { "e are" },
                if self.deprecated == 1 { "it" } else { "them" },
            ));
        }
        out
    }
}

fn text(v: &Value, key: &str) -> String {
    v.get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .trim()
        .to_owned()
}

fn flag(v: &Value, key: &str) -> bool {
    v.get(key).and_then(Value::as_bool).unwrap_or(false)
}

fn count(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

fn list<'a>(v: &'a Value, key: &str) -> impl Iterator<Item = &'a Value> {
    v.get(key)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter(|r| r.is_object())
}

fn mark(on: bool, word: &str, into: &mut Vec<String>) {
    if on {
        into.push(word.to_owned());
    }
}

fn jdk(v: &Value) -> (Row, u64) {
    let major = count(v, "version_major");
    let dotted = (major > 0).then(|| {
        format!(
            "{major}.{}.{}",
            count(v, "version_minor"),
            count(v, "version_patch")
        )
    });
    let release = text(v, "release_string");
    let version = if release.is_empty() {
        dotted.unwrap_or_default()
    } else {
        release
    };
    let vendor = text(v, "vendor");
    let mut name = if major > 0 {
        format!("Java {major}")
    } else {
        "Java".to_owned()
    };
    if !vendor.is_empty() {
        name = format!("{name} · {vendor}");
    }
    let mut marks = Vec::new();
    mark(flag(v, "is_active_java_home"), "JAVA_HOME", &mut marks);
    let row = Row {
        name,
        version,
        source: text(v, "source"),
        path: text(v, "path"),
        marks,
    };
    (row, major)
}

fn runtime(label: &str, v: &Value) -> Row {
    let version = text(v, "version");
    let mut marks = Vec::new();
    mark(flag(v, "active"), "active", &mut marks);
    Row {
        // An entry with no version names nothing and is left out.
        name: if version.is_empty() {
            String::new()
        } else {
            format!("{label} {version}")
        },
        version,
        source: text(v, "source"),
        path: text(v, "path"),
        marks,
    }
}

fn formula(v: &Value) -> Row {
    let mut marks = Vec::new();
    mark(flag(v, "pinned"), "pinned", &mut marks);
    mark(flag(v, "deprecated"), "deprecated", &mut marks);
    Row {
        name: text(v, "name"),
        version: text(v, "version"),
        source: text(v, "installed_via"),
        path: String::new(),
        marks,
    }
}

/// A row for a section this app doesn't know: whatever name-like field it has.
fn generic(label: &str, v: &Value) -> Option<Row> {
    let version = text(v, "version");
    let name = ["name", "toolchain", "channel"]
        .iter()
        .map(|k| text(v, k))
        .find(|n| !n.is_empty())
        .or_else(|| (!version.is_empty()).then(|| format!("{label} {version}")))?;
    let mut marks = Vec::new();
    mark(flag(v, "active"), "active", &mut marks);
    mark(flag(v, "default"), "default", &mut marks);
    Some(Row {
        name,
        version,
        source: text(v, "source"),
        path: text(v, "path"),
        marks,
    })
}

/// `build_tools` as `Build tools`.
fn title_of(key: &str) -> String {
    let mut s = key.replace('_', " ");
    if let Some(c) = s.get_mut(..1) {
        c.make_ascii_uppercase();
    }
    s
}

fn section(id: &str, title: &str, rows: Vec<Row>) -> Option<Section> {
    let rows: Vec<Row> = rows.into_iter().filter(|r| !r.name.is_empty()).collect();
    (!rows.is_empty()).then(|| Section {
        id: id.to_owned(),
        title: title.to_owned(),
        rows,
    })
}

/// The sections this app knows, in the order they are shown.
const RUNTIMES: [(&str, &str, &str); 4] = [
    ("node", "Node", "Node"),
    ("python", "Python", "Python"),
    ("go", "Go", "Go"),
    ("ruby", "Ruby", "Ruby"),
];

const KNOWN: [&str; 11] = [
    "jdks",
    "node",
    "python",
    "go",
    "ruby",
    "rust",
    "build_tools",
    "brew",
    "env",
    "jvm_managers",
    "active_jvm_manager",
];

/// Reads `spectra toolchain --json`. Nothing at all, or `{}`, is an Inventory with no sections.
pub fn parse(json: &str) -> Result<Inventory, String> {
    if json.trim().is_empty() {
        return Ok(Inventory::default());
    }
    let v: Value = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's toolchain list didn't read as JSON: {e}"))?;
    let Some(top) = v.as_object() else {
        return Err("Spectra's toolchain list wasn't a JSON object.".into());
    };
    let mut inv = Inventory::default();
    let mut found = Vec::new();
    let mut jdk_rows = Vec::new();
    for j in list(&v, "jdks") {
        let (row, major) = jdk(j);
        inv.jdks.push((major, row.path.clone()));
        jdk_rows.push(row);
    }
    found.extend(section("jdks", "Java", jdk_rows));
    for (key, title, label) in RUNTIMES {
        found.extend(section(
            key,
            title,
            list(&v, key).map(|r| runtime(label, r)).collect(),
        ));
    }
    let rust = list(&v, "rust")
        .map(|r| {
            let mut marks = Vec::new();
            mark(flag(r, "default"), "default", &mut marks);
            Row {
                name: text(r, "toolchain"),
                version: text(r, "channel"),
                source: "rustup".into(),
                path: String::new(),
                marks,
            }
        })
        .collect();
    found.extend(section("rust", "Rust", rust));
    let tools = list(&v, "build_tools")
        .map(|r| Row {
            name: text(r, "name"),
            version: text(r, "version"),
            source: text(r, "source"),
            path: text(r, "config_path"),
            marks: Vec::new(),
        })
        .collect();
    found.extend(section("build_tools", "Build tools", tools));
    // Sections from a newer Spectra: arrays of objects, shown as plain lists.
    for (key, val) in top.iter().filter(|(k, _)| !KNOWN.contains(&k.as_str())) {
        let label = title_of(key);
        let rows = val
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(|r| generic(&label, r))
            .collect();
        found.extend(section(key, &label, rows));
    }
    let brew = v.get("brew").unwrap_or(&Value::Null);
    let formulae: Vec<Row> = list(brew, "formulae").map(formula).collect();
    inv.deprecated = formulae
        .iter()
        .filter(|r| r.marks.iter().any(|m| m == "deprecated"))
        .count();
    found.extend(section("brew_formulae", "Homebrew formulae", formulae));
    let casks = list(brew, "casks")
        .map(|c| Row {
            name: text(c, "name"),
            version: text(c, "version"),
            source: "cask".into(),
            ..Row::default()
        })
        .collect();
    found.extend(section("brew_casks", "Homebrew casks", casks));
    let taps = list(brew, "taps")
        .map(|t| Row {
            name: text(t, "name"),
            ..Row::default()
        })
        .collect();
    found.extend(section("brew_taps", "Homebrew taps", taps));
    inv.sections = found;
    inv.java_home = v
        .get("env")
        .map(|e| text(e, "java_home"))
        .unwrap_or_default();
    Ok(inv)
}

fn failure(ran: &Ran) -> String {
    crate::compare::failure("toolchain --json", ran)
}

fn read(
    runner: &dyn Runner,
    exists: fn(&str) -> bool,
    cancel: &AtomicBool,
) -> Result<Inventory, String> {
    let ran = runner.run(&args(), DEADLINE, cancel)?;
    if ran.code != Some(0) {
        return Err(failure(&ran));
    }
    let mut inv = parse(&ran.stdout)?;
    inv.check_java_home(exists);
    Ok(inv)
}

struct Landed {
    job: u64,
    result: Result<Inventory, String>,
}

/// The Toolchains page's state.
pub struct ToolchainsPage {
    pub runner: Arc<dyn Runner>,
    /// Off in tests: the read runs on the calling thread and lands on the next `land_toolchains`.
    pub threaded: bool,
    /// Looks for a folder; tests and sample scenes swap it so no disk is read.
    pub exists: fn(&str) -> bool,
    /// The last good reading and when it landed on the window clock.
    pub data: Option<(Inventory, f64)>,
    pub error: Option<String>,
    pub query: TextEdit,
    /// Sections whose open state was flipped from how they start.
    pub toggled: HashSet<String>,
    reading: bool,
    /// The page opened while the window was hidden: it reads once the window is shown.
    wanted: bool,
    /// A read was cut off by leaving the page: the next open reads again.
    cut: bool,
    job: u64,
    cancel: Arc<AtomicBool>,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
}

impl Default for ToolchainsPage {
    fn default() -> ToolchainsPage {
        ToolchainsPage::new()
    }
}

impl ToolchainsPage {
    pub fn new() -> ToolchainsPage {
        let (tx, rx) = channel();
        ToolchainsPage {
            runner: Arc::new(SpectraBin),
            threaded: true,
            exists: |p| std::path::Path::new(p).exists(),
            data: None,
            error: None,
            query: TextEdit::new(""),
            toggled: HashSet::new(),
            reading: false,
            wanted: false,
            cut: false,
            job: 0,
            cancel: Arc::new(AtomicBool::new(false)),
            tx,
            rx,
        }
    }

    pub fn reading(&self) -> bool {
        self.reading
    }

    /// The search words, lower-cased.
    pub fn search(&self) -> String {
        self.query.text.trim().to_lowercase()
    }

    /// Whether `s` shows its rows: while searching, every section that has a match; otherwise
    /// how it starts, flipped by a press on its header.
    pub fn is_open(&self, s: &Section) -> bool {
        if !self.search().is_empty() {
            return true;
        }
        s.default_open() != self.toggled.contains(&s.id)
    }

    /// A reading that never lands, so a sample scene shows the page reading.
    pub fn sample_reading(&mut self) {
        self.reading = true;
    }
}

impl App {
    /// The page opened: reads once if nothing has been read, and does nothing on a return to
    /// a page that already has its reading (or its failure; Refresh tries again).
    pub fn open_toolchains(&mut self) {
        let t = &self.toolchains;
        if t.reading || ((t.data.is_some() || t.error.is_some()) && !t.cut) {
            return;
        }
        self.refresh_toolchains();
    }

    /// Reads now, on a press of Refresh; a read already on its way is left to finish.
    pub fn refresh_toolchains(&mut self) {
        let shown = self.window_shown();
        let wake = self.wake.clone();
        let t = &mut self.toolchains;
        if t.reading {
            return;
        }
        if !shown {
            t.wanted = true;
            return;
        }
        t.wanted = false;
        t.cut = false;
        t.reading = true;
        t.job += 1;
        t.cancel = Arc::new(AtomicBool::new(false));
        let (job, cancel, runner, exists) = (
            t.job,
            Arc::clone(&t.cancel),
            Arc::clone(&t.runner),
            t.exists,
        );
        let tx = t.tx.clone();
        if !t.threaded {
            let result = read(runner.as_ref(), exists, &cancel);
            let _ = tx.send(Landed { job, result });
            return;
        }
        // Quit waits for this from now until it's done, even before the thread starts.
        let hold = crate::run::pending();
        let result_tx = Arc::new(Mutex::new(Some(tx.clone())));
        let on_thread = Arc::clone(&result_tx);
        let started = std::thread::Builder::new()
            .name("spectra-toolchains".into())
            .spawn(move || {
                let landed = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    read(runner.as_ref(), exists, &cancel)
                }))
                .unwrap_or_else(|_| Err("the working thread failed".into()));
                if let Some(tx) = on_thread.lock().ok().and_then(|mut t| t.take()) {
                    let _ = tx.send(Landed {
                        job,
                        result: landed,
                    });
                }
                drop(hold);
                if let Some(wake) = wake {
                    wake();
                }
            });
        if let Err(e) = started {
            let _ = tx.send(Landed {
                job,
                result: Err(format!("Couldn't start a thread to read toolchains: {e}.")),
            });
        }
    }

    /// Opens a closed section and closes an open one. While searching every matching section is
    /// open, so a press there changes nothing.
    pub fn toggle_toolchain_section(&mut self, id: &str) {
        if !self.toolchains.search().is_empty() {
            return;
        }
        let t = &mut self.toolchains.toggled;
        if !t.remove(id) {
            t.insert(id.to_owned());
        }
    }

    /// Stops a read on its way, on leaving the page and on quit: its answer is dropped, and
    /// opening the page again reads again.
    pub fn cancel_toolchains(&mut self) {
        let t = &mut self.toolchains;
        t.cancel.store(true, Ordering::SeqCst);
        t.cut |= t.reading || t.wanted;
        t.reading = false;
        t.wanted = false;
    }

    /// Lands what finished, and reads a page that was opened while the window was hidden once
    /// the window is shown. True when anything shown changed.
    pub fn tick_toolchains(&mut self) -> bool {
        let mut changed = self.land_toolchains();
        if self.toolchains.wanted && self.window_shown() {
            self.refresh_toolchains();
            changed = true;
        }
        changed
    }

    /// Lands a finished read. True when one did.
    pub fn land_toolchains(&mut self) -> bool {
        let mut landed = false;
        let now = self.now;
        while let Ok(l) = self.toolchains.rx.try_recv() {
            let t = &mut self.toolchains;
            if !t.reading || l.job != t.job {
                continue;
            }
            landed = true;
            t.reading = false;
            match l.result {
                Ok(inv) => {
                    t.data = Some((inv, now));
                    t.error = None;
                }
                Err(why) => t.error = Some(why),
            }
        }
        landed
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    use crate::sample_toolchains::{EMPTY, FULL, SPARSE};

    /// Answers `toolchain --json` from a script and records every command.
    pub struct Fake {
        pub calls: Mutex<Vec<Vec<String>>>,
        pub answer: Result<Ran, String>,
    }

    pub fn ok(stdout: &str) -> Result<Ran, String> {
        Ok(Ran {
            code: Some(0),
            stdout: stdout.into(),
            stderr: String::new(),
        })
    }

    pub fn failed(code: i32, stderr: &str) -> Result<Ran, String> {
        Ok(Ran {
            code: Some(code),
            stdout: String::new(),
            stderr: stderr.into(),
        })
    }

    impl Fake {
        pub fn new(answer: Result<Ran, String>) -> Fake {
            Fake {
                calls: Mutex::new(Vec::new()),
                answer,
            }
        }

        pub fn count(&self) -> usize {
            self.calls.lock().unwrap().len()
        }
    }

    impl Runner for Fake {
        fn run(&self, args: &[String], _: Duration, _: &AtomicBool) -> Result<Ran, String> {
            self.calls.lock().unwrap().push(args.to_vec());
            self.answer.clone()
        }
    }

    fn app(fake: &Arc<Fake>) -> App {
        let mut s = crate::app::init();
        s.toolchains.runner = fake.clone();
        s.toolchains.threaded = false;
        s.toolchains.exists = |_| true;
        s.visible = true;
        s
    }

    fn ids(inv: &Inventory) -> Vec<&str> {
        inv.sections.iter().map(|s| s.id.as_str()).collect()
    }

    #[test]
    fn the_fixture_reads_in_the_order_the_page_shows() {
        let inv = parse(FULL).unwrap();
        assert_eq!(
            ids(&inv),
            [
                "jdks",
                "node",
                "python",
                "go",
                "ruby",
                "rust",
                "build_tools",
                "brew_formulae",
                "brew_casks",
                "brew_taps"
            ]
        );
        let java = &inv.sections[0];
        assert_eq!(java.rows.len(), 3);
        assert_eq!(java.rows[0].name, "Java 17 · Eclipse Adoptium");
        assert_eq!(java.rows[0].version, "17.0.13+11");
        assert_eq!(java.rows[2].marks, ["JAVA_HOME"]);
        let node = &inv.sections[1];
        assert_eq!(node.rows[0].name, "Node 22.11.0");
        assert_eq!(node.rows[0].marks, ["active"]);
        let brew = &inv.sections[7];
        assert_eq!(brew.rows.len(), 8);
        assert_eq!(brew.rows[4].marks, ["pinned"]);
        assert_eq!(brew.rows[6].source, "tap");
        assert_eq!(inv.sections[8].rows.len(), 2);
        assert_eq!(inv.total(), 3 + 2 + 2 + 1 + 1 + 2 + 3 + 8 + 2 + 2);
        let rust = &inv.sections[5];
        assert_eq!(rust.rows[0].marks, ["default"]);
    }

    #[test]
    fn missing_fields_an_unknown_section_and_stray_values_never_fail_the_read() {
        let inv = parse(SPARSE).unwrap();
        assert_eq!(ids(&inv), ["jdks", "node", "zig"]);
        let java = &inv.sections[0];
        assert_eq!(java.rows[0].name, "Java");
        assert_eq!(java.rows[0].path, "/Users/user/jdks/jdk-11");
        assert_eq!(java.rows[1].name, "Java 8");
        // An empty node entry has no name or version: it isn't a row.
        assert_eq!(inv.sections[1].rows.len(), 1);
        assert_eq!(inv.sections[1].rows[0].name, "Node 20.1.0");
        // The unknown section is a plain list, named from its key.
        let zig = &inv.sections[2];
        assert_eq!(zig.title, "Zig");
        assert_eq!(zig.rows[0].name, "Zig 0.13.0");
        assert_eq!(zig.rows[1].name, "zls");
    }

    #[test]
    fn nothing_printed_or_an_empty_object_is_no_sections_and_other_json_is_an_error() {
        assert!(parse("").unwrap().sections.is_empty());
        assert!(parse(EMPTY).unwrap().sections.is_empty());
        assert!(parse("[1]").is_err());
        assert!(
            parse("not json")
                .unwrap_err()
                .contains("didn't read as JSON")
        );
    }

    #[test]
    fn a_search_keeps_rows_whose_name_version_or_source_has_every_word() {
        let inv = parse(FULL).unwrap();
        let brew = &inv.sections[7];
        let names =
            |q: &str| -> Vec<&str> { brew.matching(q).iter().map(|r| r.name.as_str()).collect() };
        assert_eq!(names("").len(), 8);
        assert_eq!(names("rip"), ["ripgrep"]);
        assert_eq!(names("tap"), ["tool-from-tap"]);
        assert_eq!(names("21 openjdk"), ["openjdk@21"]);
        assert!(names("no such formula").is_empty());
    }

    #[test]
    fn several_jdks_of_one_major_version_are_flagged() {
        let inv = parse(FULL).unwrap();
        let flags = inv.flags();
        assert!(
            flags
                .iter()
                .any(|f| f.starts_with("Java 17 is installed 2 times")),
            "{flags:?}"
        );
        assert!(!flags.iter().any(|f| f.contains("Java 21")), "{flags:?}");
        // The deprecated formula is flagged too.
        assert!(
            flags
                .iter()
                .any(|f| f.contains("1 installed Homebrew formula is deprecated"))
        );
    }

    #[test]
    fn a_java_home_that_is_gone_is_flagged_and_one_that_is_there_is_not() {
        let mut inv = parse(FULL).unwrap();
        assert!(
            !inv.flags().iter().any(|f| f.contains("JAVA_HOME points")),
            "unchecked says nothing"
        );
        inv.check_java_home(|_| true);
        assert!(!inv.flags().iter().any(|f| f.contains("JAVA_HOME points")));
        inv.check_java_home(|p| {
            p != "/Users/user/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home"
        });
        assert!(inv.flags().iter().any(|f| {
            f.starts_with("JAVA_HOME points to /Users/user/Library/Java")
                && f.contains("isn't there")
        }));
        // Sparse data names a JAVA_HOME with no JDK listed for it.
        let mut sparse = parse(SPARSE).unwrap();
        sparse.check_java_home(|_| false);
        assert!(
            sparse
                .flags()
                .iter()
                .any(|f| f.contains("/Users/user/gone/jdk-17"))
        );
        // No JAVA_HOME set: nothing to check.
        let mut none = parse(EMPTY).unwrap();
        none.check_java_home(|_| false);
        assert!(none.flags().is_empty());
    }

    #[test]
    fn jdks_and_runtimes_start_open_and_homebrew_starts_closed_until_toggled() {
        let fake = Arc::new(Fake::new(ok(FULL)));
        let mut s = app(&fake);
        s.open_toolchains();
        s.land_toolchains();
        let inv = &s.toolchains.data.as_ref().unwrap().0;
        let open: Vec<(&str, bool)> = inv
            .sections
            .iter()
            .map(|x| (x.id.as_str(), s.toolchains.is_open(x)))
            .collect();
        assert!(open[..7].iter().all(|(_, o)| *o), "{open:?}");
        assert!(open[7..].iter().all(|(_, o)| !*o), "{open:?}");
        s.toggle_toolchain_section("brew_formulae");
        s.toggle_toolchain_section("node");
        let inv = &s.toolchains.data.as_ref().unwrap().0;
        assert!(s.toolchains.is_open(&inv.sections[7]));
        assert!(!s.toolchains.is_open(&inv.sections[1]));
        s.toggle_toolchain_section("node");
        let inv = &s.toolchains.data.as_ref().unwrap().0;
        assert!(s.toolchains.is_open(&inv.sections[1]));
        // A search opens every section.
        s.toolchains.query = TextEdit::new("rip");
        assert!(s.toolchains.is_open(&inv.sections[8]));
    }

    #[test]
    fn opening_reads_once_and_a_return_reads_nothing_until_refresh() {
        let fake = Arc::new(Fake::new(ok(FULL)));
        let mut s = app(&fake);
        s.show(crate::groups::Nav::Toolchains);
        assert_eq!(fake.count(), 1);
        assert_eq!(
            fake.calls.lock().unwrap()[0],
            ["toolchain".to_owned(), "--json".to_owned()]
        );
        assert!(s.toolchains.reading());
        s.land_toolchains();
        assert!(!s.toolchains.reading());
        s.show(crate::groups::Nav::All);
        s.show(crate::groups::Nav::Toolchains);
        s.show(crate::groups::Nav::Toolchains);
        assert_eq!(fake.count(), 1, "a return to the page doesn't read");
        s.refresh_toolchains();
        s.land_toolchains();
        assert_eq!(fake.count(), 2);
        // Ticks read nothing either.
        for _ in 0..5 {
            s.tick_toolchains();
        }
        assert_eq!(fake.count(), 2);
    }

    #[test]
    fn a_refresh_cut_off_by_leaving_reads_again_on_return() {
        let fake = Arc::new(Fake::new(ok(FULL)));
        let mut s = app(&fake);
        s.show(crate::groups::Nav::Toolchains);
        s.land_toolchains();
        s.refresh_toolchains();
        assert_eq!(fake.count(), 2);
        s.show(crate::groups::Nav::All);
        s.show(crate::groups::Nav::Toolchains);
        assert_eq!(fake.count(), 3, "the cut-off refresh is read again");
        s.land_toolchains();
        s.show(crate::groups::Nav::All);
        s.show(crate::groups::Nav::Toolchains);
        assert_eq!(fake.count(), 3, "a finished one isn't");
    }

    #[test]
    fn a_header_press_while_searching_changes_nothing() {
        let fake = Arc::new(Fake::new(ok(FULL)));
        let mut s = app(&fake);
        s.toolchains.query = TextEdit::new("jq");
        s.toggle_toolchain_section("brew_formulae");
        assert!(s.toolchains.toggled.is_empty());
        s.toolchains.query = TextEdit::new("");
        s.toggle_toolchain_section("brew_formulae");
        assert!(s.toolchains.toggled.contains("brew_formulae"));
    }

    #[test]
    fn nothing_is_read_while_the_window_is_hidden() {
        let fake = Arc::new(Fake::new(ok(FULL)));
        let mut s = app(&fake);
        s.visible = false;
        s.show(crate::groups::Nav::Toolchains);
        assert_eq!(fake.count(), 0);
        s.tick_toolchains();
        assert_eq!(fake.count(), 0);
        s.visible = true;
        s.tick_toolchains();
        assert_eq!(fake.count(), 1);
        s.tick_toolchains();
        assert!(s.toolchains.data.is_some());
        assert_eq!(fake.count(), 1);
    }

    #[test]
    fn a_failed_read_says_why_keeps_the_old_reading_and_refresh_tries_again() {
        let fake = Arc::new(Fake::new(failed(1, "spectra: brew not found\n")));
        let mut s = app(&fake);
        s.open_toolchains();
        s.land_toolchains();
        assert_eq!(
            s.toolchains.error.as_deref(),
            Some("spectra: brew not found")
        );
        assert!(s.toolchains.data.is_none());
        // Returning doesn't retry; Refresh does.
        s.open_toolchains();
        assert_eq!(fake.count(), 1);
        let good = Arc::new(Fake::new(ok(FULL)));
        s.toolchains.runner = good.clone();
        s.refresh_toolchains();
        s.land_toolchains();
        assert!(s.toolchains.error.is_none());
        assert!(s.toolchains.data.is_some());
        // A later failure keeps what was read.
        s.toolchains.runner = Arc::new(Fake::new(failed(2, "")));
        s.refresh_toolchains();
        s.land_toolchains();
        assert!(s.toolchains.data.is_some());
        assert_eq!(
            s.toolchains.error.as_deref(),
            Some("spectra toolchain --json stopped with exit status 2")
        );
    }

    #[test]
    fn leaving_the_page_drops_a_read_on_its_way_and_opening_reads_again() {
        let fake = Arc::new(Fake::new(ok(FULL)));
        let mut s = app(&fake);
        s.show(crate::groups::Nav::Toolchains);
        s.show(crate::groups::Nav::All);
        assert!(!s.toolchains.reading());
        assert!(!s.land_toolchains(), "the dropped answer lands nowhere");
        assert!(s.toolchains.data.is_none());
        s.show(crate::groups::Nav::Toolchains);
        assert_eq!(fake.count(), 2);
    }
}
