//! The code view's job and panel state, with a fake runner and filesystem; the real runner's
//! deadline and cap on stock commands; and one live lldb run, ignored by default.

use super::*;
use crate::codeview_tools::tests::FakeProbe;
use std::collections::HashMap;

const LLDB: &str = include_str!("../tests/fixtures/codeview/lldb-native.txt");
const R2_NO_PLUGIN: &str = include_str!("../tests/fixtures/codeview/r2-no-plugin.txt");
const PSEUDO: &str = include_str!("../tests/fixtures/codeview/pseudo-c.txt");

/// Answers by program name; records what ran, what was written and removed. A program set to
/// block waits until it's cancelled.
#[derive(Default)]
pub struct FakeRunner {
    pub replies: HashMap<String, Result<String, String>>,
    pub files: Mutex<HashMap<PathBuf, String>>,
    pub calls: Mutex<Vec<(String, Vec<String>)>>,
    pub removed: Mutex<Vec<PathBuf>>,
    pub block: Option<String>,
    /// Folders made.
    pub dirs: Mutex<Vec<PathBuf>>,
    /// A program that panics when run.
    pub panics: Option<String>,
    /// A program that takes this long.
    pub slow: Option<(String, Duration)>,
}

impl FakeRunner {
    pub fn new(replies: &[(&str, Result<&str, &str>)]) -> FakeRunner {
        FakeRunner {
            replies: replies
                .iter()
                .map(|(k, v)| ((*k).to_owned(), v.map(str::to_owned).map_err(str::to_owned)))
                .collect(),
            ..FakeRunner::default()
        }
    }

    fn ran(&self) -> Vec<String> {
        self.calls
            .lock()
            .unwrap()
            .iter()
            .map(|(p, _)| p.clone())
            .collect()
    }
}

impl Runner for FakeRunner {
    fn run(
        &self,
        program: &Path,
        args: &[String],
        _: Duration,
        _: usize,
        cancel: &AtomicBool,
    ) -> Result<String, Failure> {
        let name = program.file_name().unwrap().to_string_lossy().into_owned();
        self.calls
            .lock()
            .unwrap()
            .push((name.clone(), args.to_vec()));
        if self.block.as_deref() == Some(name.as_str()) {
            while !cancel.load(Ordering::SeqCst) {
                std::thread::sleep(Duration::from_millis(5));
            }
            return Err(Failure::Cancelled);
        }
        if self.panics.as_deref() == Some(name.as_str()) {
            panic!("{name} fell over");
        }
        if let Some((slow, d)) = &self.slow
            && *slow == name
        {
            std::thread::sleep(*d);
        }
        // `java` running Fernflower: it writes into an `out` folder that must already exist.
        if name == "java"
            && let Some(out) = args.iter().find(|a| a.ends_with("/out"))
        {
            let out = PathBuf::from(out);
            if !self.dirs.lock().unwrap().contains(&out) {
                return Err(Failure::Failed(format!(
                    "java: destination {} isn't a folder",
                    out.display()
                )));
            }
            if let Some(Ok(text)) = self.replies.get(&name) {
                self.files
                    .lock()
                    .unwrap()
                    .insert(out.join("Main.java"), text.clone());
            }
            return Ok(String::new());
        }
        // A tool that writes its answer to a file in its temp folder.
        if let Some(out) = args.iter().find(|a| a.ends_with("/out.c")) {
            if let Some(Ok(text)) = self.replies.get(&name) {
                self.files
                    .lock()
                    .unwrap()
                    .insert(PathBuf::from(out), text.clone());
            }
            return Ok(String::new());
        }
        match self.replies.get(&name) {
            Some(Ok(s)) => Ok(s.clone()),
            Some(Err(e)) => Err(Failure::Failed(e.clone())),
            None => Err(Failure::Failed(format!("Couldn't run {name}"))),
        }
    }
    fn read(&self, path: &Path, _: usize) -> Result<String, String> {
        self.files
            .lock()
            .unwrap()
            .get(path)
            .cloned()
            .ok_or_else(|| format!("Couldn't open {}", path.display()))
    }
    fn temp_dir(&self) -> Result<PathBuf, String> {
        Ok(PathBuf::from("/tmp/fake-codeview-0"))
    }
    fn write(&self, path: &Path, text: &str) -> Result<(), String> {
        self.files
            .lock()
            .unwrap()
            .insert(path.to_path_buf(), text.to_owned());
        Ok(())
    }
    fn make_dir(&self, path: &Path) -> Result<(), String> {
        self.dirs.lock().unwrap().push(path.to_path_buf());
        Ok(())
    }
    fn remove_temp(&self, dir: &Path) {
        self.removed.lock().unwrap().push(dir.to_path_buf());
    }
    fn find(&self, dir: &Path, ext: &str) -> Option<PathBuf> {
        self.files
            .lock()
            .unwrap()
            .keys()
            .find(|k| k.starts_with(dir) && k.extension().is_some_and(|e| e == ext))
            .cloned()
    }
}

const APP: &str = "/Applications/Sketchpad.app/Contents/MacOS/Sketchpad";
const INK: &str = "/Applications/Sketchpad.app/Contents/Frameworks/InkKit.framework/InkKit";

fn crash(frame: &str) -> Request {
    Request::Crash {
        frame: frame.into(),
        process_path: APP.into(),
    }
}

fn cache_of(t: Tools) -> Mutex<Option<Arc<Tools>>> {
    Mutex::new(Some(Arc::new(t)))
}

#[test]
fn the_first_step_that_shows_code_wins_and_earlier_failures_are_listed() {
    let r = FakeRunner::new(&[("r2", Ok(R2_NO_PLUGIN)), ("lldb", Ok(LLDB))]);
    let p = FakeProbe::new(&[], &[APP, INK]);
    let tools = Tools::with(&[
        (Tool::R2, "/opt/homebrew/bin/r2"),
        (Tool::Lldb, "/usr/bin/lldb"),
    ]);
    let state = work(
        &crash("InkKit`ink_stroke_commit + 344"),
        &r,
        &p,
        &cache_of(tools),
        &AtomicBool::new(false),
    );
    let Some(State::Shown(sh)) = state else {
        panic!("code shows: {state:?}")
    };
    assert_eq!(sh.what, "Disassembly from lldb");
    assert_eq!(r.ran(), ["r2", "r2", "lldb"]);
    assert_eq!(sh.tried.len(), 2);
    assert!(sh.tried[0].starts_with("radare2: the decompiler gave no code"));
    assert!(sh.mark.is_some());
}

#[test]
fn a_decompiler_writing_a_file_is_read_and_its_temp_folder_removed() {
    let r = FakeRunner::new(&[("retdec-decompiler", Ok(PSEUDO))]);
    let p = FakeProbe::new(&[], &[APP, INK]);
    let tools = Tools::with(&[(Tool::RetDec, "/r/retdec-decompiler")]);
    let state = work(
        &crash("InkKit`ink_stroke_commit + 8"),
        &r,
        &p,
        &cache_of(tools),
        &AtomicBool::new(false),
    );
    let Some(State::Shown(sh)) = state else {
        panic!("{state:?}")
    };
    assert_eq!(sh.what, "Pseudo-C decompiled by RetDec");
    let (_, args) = r.calls.lock().unwrap()[0].clone();
    assert!(args.contains(&"/tmp/fake-codeview-0/out.c".to_owned()));
    assert_eq!(
        *r.removed.lock().unwrap(),
        [PathBuf::from("/tmp/fake-codeview-0")]
    );
}

#[test]
fn ghidra_gets_its_script_written_first() {
    let r = FakeRunner::new(&[("analyzeHeadless", Ok(PSEUDO))]);
    let p = FakeProbe::new(&[], &[APP, INK]);
    let tools = Tools::with(&[(Tool::Ghidra, "/g/support/analyzeHeadless")]);
    let state = work(
        &crash("InkKit`ink_stroke_commit + 8"),
        &r,
        &p,
        &cache_of(tools),
        &AtomicBool::new(false),
    );
    assert!(matches!(state, Some(State::Shown(_))), "{state:?}");
    let files = r.files.lock().unwrap();
    let script = files
        .get(Path::new("/tmp/fake-codeview-0/CodeViewDecompile.java"))
        .expect("the script was written into the temp folder");
    assert!(script.contains("class CodeViewDecompile extends GhidraScript"));
    let (_, args) = r.calls.lock().unwrap()[0].clone();
    assert_eq!(args[0], "/tmp/fake-codeview-0");
    assert!(!args.iter().any(|a| a.contains(crate::codeview_plan::TMP)));
}

#[test]
fn every_step_failing_says_each_reason() {
    let r = FakeRunner::new(&[("lldb", Ok("(lldb) script exec(…)\nCODEVIEW_NONE\n"))]);
    let p = FakeProbe::new(&[], &[APP]);
    let tools = Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]);
    let Some(State::Nothing(why)) = work(
        &crash("AppKit`-[NSView display] + 12"),
        &r,
        &p,
        &cache_of(tools),
        &AtomicBool::new(false),
    ) else {
        panic!()
    };
    assert!(
        why.contains("lldb found no function named -[NSView display] in AppKit"),
        "{why}"
    );
}

#[test]
fn nothing_found_looks_for_tools_again() {
    let r = FakeRunner::new(&[("lldb", Ok(LLDB))]);
    // The cache says nothing was found; lldb has been installed since.
    let p = FakeProbe::new(&["/usr/bin"], &[APP, "/usr/bin/lldb"]);
    let cache = cache_of(Tools::default());
    let state = work(
        &crash("AppKit`-[NSView display] + 12"),
        &r,
        &p,
        &cache,
        &AtomicBool::new(false),
    );
    assert!(matches!(state, Some(State::Shown(_))), "{state:?}");
    assert!(cache.lock().unwrap().as_ref().unwrap().has(Tool::Lldb));
    // A frame with nothing to look up doesn't look again.
    let cache = cache_of(Tools::default());
    let Some(State::Nothing(why)) = work(&crash("???"), &r, &p, &cache, &AtomicBool::new(false))
    else {
        panic!()
    };
    assert!(why.contains("which binary"));
    assert_eq!(**cache.lock().unwrap().as_ref().unwrap(), Tools::default());
}

#[test]
fn a_minified_file_is_pretty_printed_and_a_plain_one_read_as_is() {
    let plain = "/srv/web/a.js";
    let mini = "/srv/web/b.min.js";
    let r = FakeRunner::new(&[("js-beautify", Ok("function go() {\n  return 1;\n}\n"))]);
    r.files
        .lock()
        .unwrap()
        .insert(plain.into(), "let a = 1;\nlet b = 2;\n".into());
    r.files.lock().unwrap().insert(
        mini.into(),
        format!("!function(){{{}}}();", "var a=1;".repeat(300)),
    );
    let p = FakeProbe::new(&[], &[plain, mini]);
    let tools = Tools::with(&[(Tool::JsBeautify, "/opt/homebrew/bin/js-beautify")]);
    let cache = cache_of(tools);
    let go = |f: &str| {
        work(
            &Request::Script { frame: f.into() },
            &r,
            &p,
            &cache,
            &AtomicBool::new(false),
        )
    };
    let Some(State::Shown(a)) = go("at /srv/web/a.js:2:1") else {
        panic!()
    };
    assert_eq!(a.what, "Source file");
    assert_eq!(a.mark, Some(1));
    let Some(State::Shown(b)) = go("at /srv/web/b.min.js:1:900") else {
        panic!()
    };
    assert_eq!(b.what, "Source file, pretty-printed by js-beautify");
    assert_eq!(r.ran(), ["js-beautify"]);
}

#[test]
fn objdump_output_is_demangled() {
    let objdump = include_str!("../tests/fixtures/codeview/objdump.txt");
    let r = FakeRunner::new(&[
        ("llvm-objdump", Ok(objdump)),
        (
            "swift-demangle",
            Ok("Ledger.Entry.hash.getter : Swift.Int\n"),
        ),
        ("c++filt", Ok("ledger::Store::flush()\n")),
    ]);
    let bin = "/opt/tools/bin/ledgerd";
    let p = FakeProbe::new(&[], &[bin]);
    let tools = Tools::with(&[
        (Tool::Objdump, "/x/llvm-objdump"),
        (Tool::SwiftDemangle, "/x/swift-demangle"),
        (Tool::CxxFilt, "/usr/bin/c++filt"),
    ]);
    let req = Request::Crash {
        frame: "ledgerd`ledger_flush + 24".into(),
        process_path: bin.into(),
    };
    let Some(State::Shown(sh)) = work(&req, &r, &p, &cache_of(tools), &AtomicBool::new(false))
    else {
        panic!()
    };
    let text = sh.lines.join("\n");
    assert!(text.contains("Ledger.Entry.hash.getter : Swift.Int"));
    assert!(text.contains("ledger::Store::flush()"));
    let calls = r.calls.lock().unwrap().clone();
    assert_eq!(
        calls[1],
        (
            "swift-demangle".into(),
            vec!["--compact".into(), "_$s6Ledger5EntryV4hashSivg".into()]
        )
    );
}

const MAIN_CLASS: &str = "/srv/checkout/classes/com/acme/checkout/Main.class";

fn java_main() -> Request {
    Request::Java {
        frame: "com.acme.checkout.Main.main(Main.java:9)".into(),
        command_line: "java -cp /srv/checkout/classes com.acme.checkout.Main".into(),
        exe: String::new(),
    }
}

#[test]
fn fernflower_gets_its_out_folder_made_first() {
    let src = "package com.acme.checkout;\n\npublic class Main {\n    public static void main(String[] args) {\n        run();\n    }\n}\n";
    let r = FakeRunner::new(&[("java", Ok(src))]);
    let p = FakeProbe::new(&[], &[MAIN_CLASS]);
    let tools = Tools::with(&[
        (Tool::Java, "/usr/bin/java"),
        (Tool::FernflowerJar, "/ide/java-decompiler.jar"),
    ]);
    let state = work(
        &java_main(),
        &r,
        &p,
        &cache_of(tools),
        &AtomicBool::new(false),
    );
    let Some(State::Shown(sh)) = state else {
        panic!("{state:?}")
    };
    assert_eq!(sh.what, "Java decompiled by Fernflower");
    assert_eq!(
        *r.dirs.lock().unwrap(),
        [PathBuf::from("/tmp/fake-codeview-0/out")]
    );
}

#[test]
fn demangling_is_skipped_once_the_time_is_gone() {
    let objdump = include_str!("../tests/fixtures/codeview/objdump.txt");
    let mut r = FakeRunner::new(&[
        ("llvm-objdump", Ok(objdump)),
        (
            "swift-demangle",
            Ok("Ledger.Entry.hash.getter : Swift.Int\n"),
        ),
    ]);
    // The disassembler uses up the whole budget.
    r.slow = Some(("llvm-objdump".into(), Duration::from_millis(300)));
    let bin = "/opt/tools/bin/ledgerd";
    let p = FakeProbe::new(&[], &[bin]);
    let tools = Tools::with(&[
        (Tool::Objdump, "/x/llvm-objdump"),
        (Tool::SwiftDemangle, "/x/swift-demangle"),
    ]);
    let req = Request::Crash {
        frame: "ledgerd`ledger_flush + 24".into(),
        process_path: bin.into(),
    };
    let state = work_within(
        &req,
        &r,
        &p,
        &cache_of(tools),
        &AtomicBool::new(false),
        Duration::from_millis(200),
    );
    assert!(matches!(state, Some(State::Shown(_))), "{state:?}");
    assert_eq!(r.ran(), ["llvm-objdump"]);
}

#[test]
fn looking_up_a_class_stops_between_jars_once_cancelled() {
    let jars = [
        "/srv/checkout/lib/a.jar",
        "/srv/checkout/lib/b.jar",
        "/srv/checkout/lib/c.jar",
    ];
    let p = FakeProbe::new(&[], &jars);
    let cancel = AtomicBool::new(false);
    /// Cancels the job on the first jar it reads.
    struct Cancelling<'a>(&'a FakeProbe, &'a AtomicBool);
    impl Probe for Cancelling<'_> {
        fn path_dirs(&self) -> Vec<PathBuf> {
            self.0.path_dirs()
        }
        fn home(&self) -> Option<PathBuf> {
            self.0.home()
        }
        fn var(&self, n: &str) -> Option<String> {
            self.0.var(n)
        }
        fn is_file(&self, f: &Path) -> bool {
            self.0.is_file(f)
        }
        fn list(&self, d: &Path) -> Vec<PathBuf> {
            self.0.list(d)
        }
        fn xcrun(&self, n: &str) -> Option<PathBuf> {
            self.0.xcrun(n)
        }
        fn jar_has(&self, jar: &Path, entry: &str) -> bool {
            self.1.store(true, Ordering::SeqCst);
            self.0.jar_has(jar, entry)
        }
        fn canonical(&self, f: &Path) -> Option<PathBuf> {
            self.0.canonical(f)
        }
        fn owner_mode(&self, f: &Path) -> Option<(u32, u32)> {
            self.0.owner_mode(f)
        }
        fn uid(&self) -> u32 {
            self.0.uid()
        }
        fn java_home(&self) -> Option<PathBuf> {
            self.0.java_home()
        }
    }
    let req = Request::Java {
        frame: "com.acme.checkout.Main.main(Main.java:9)".into(),
        command_line: "java -cp /srv/checkout/lib/* com.acme.checkout.Main".into(),
        exe: String::new(),
    };
    let state = work(
        &req,
        &FakeRunner::new(&[]),
        &Cancelling(&p, &cancel),
        &cache_of(Tools::with(&[(Tool::Javap, "/usr/bin/javap")])),
        &cancel,
    );
    assert_eq!(state, None);
    assert_eq!(
        *p.jars_read.lock().unwrap(),
        1,
        "no jar read once cancelled"
    );
}

#[test]
fn a_panicking_job_removes_its_temp_folder_and_lands_a_failure() {
    let mut r = FakeRunner::new(&[]);
    r.panics = Some("retdec-decompiler".into());
    let (mut s, r) = app_with(
        r,
        FakeProbe::new(&[], &[APP, INK]),
        Tools::with(&[(Tool::RetDec, "/r/retdec-decompiler")]),
    );
    s.codeview.threaded = false;
    s.show_crash_code("InkKit`ink_stroke_commit + 8", APP);
    assert_eq!(
        *r.removed.lock().unwrap(),
        [PathBuf::from("/tmp/fake-codeview-0")]
    );
    assert!(s.land_codeview());
    assert_eq!(
        s.codeview.open.as_ref().unwrap().state,
        State::Nothing("The code view stopped unexpectedly.".into())
    );
}

fn app_with(r: FakeRunner, p: FakeProbe, tools: Tools) -> (App, Arc<FakeRunner>) {
    let mut s = crate::app::init();
    let r = Arc::new(r);
    s.codeview.runner = r.clone();
    s.codeview.probe = Arc::new(p);
    s.codeview.set_tools(tools);
    (s, r)
}

#[test]
fn a_press_opens_the_panel_and_lands_the_code() {
    let (mut s, _) = app_with(
        FakeRunner::new(&[("lldb", Ok(LLDB))]),
        FakeProbe::new(&[], &[APP]),
        Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]),
    );
    s.codeview.threaded = false;
    s.show_crash_code("AppKit`-[NSView display] + 12", APP);
    assert_eq!(s.codeview.open.as_ref().unwrap().state, State::Running);
    assert!(s.land_codeview());
    let p = s.codeview.open.as_ref().unwrap();
    assert_eq!(p.title, "AppKit`-[NSView display] + 12");
    let State::Shown(sh) = &p.state else {
        panic!("{:?}", p.state)
    };
    // The fixture's 99 lines less lldb's echo and the marker.
    assert_eq!(sh.lines.len(), 97);
}

#[test]
fn closing_the_panel_stops_its_tool() {
    let mut r = FakeRunner::new(&[]);
    r.block = Some("lldb".into());
    let (mut s, r) = app_with(
        r,
        FakeProbe::new(&[], &[APP]),
        Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]),
    );
    s.show_crash_code("AppKit`-[NSView display] + 12", APP);
    // Wait for lldb to start.
    let start = Instant::now();
    while r.ran().is_empty() && start.elapsed() < Duration::from_secs(5) {
        std::thread::sleep(Duration::from_millis(5));
    }
    assert_eq!(r.ran(), ["lldb"]);
    let cancel = Arc::clone(&s.codeview.flight.as_ref().unwrap().1);
    s.close_code();
    assert!(cancel.load(Ordering::SeqCst));
    assert!(s.codeview.open.is_none());
    // The cancelled job ends and lands nothing.
    let l = s
        .codeview
        .rx
        .recv_timeout(Duration::from_secs(5))
        .expect("the job ended");
    assert!(l.answer.is_none());
    assert!(!s.land_codeview());
    assert!(s.codeview.open.is_none());
}

#[test]
fn another_page_closes_the_panel_and_stops_its_tool() {
    let mut r = FakeRunner::new(&[]);
    r.block = Some("lldb".into());
    let (mut s, _) = app_with(
        r,
        FakeProbe::new(&[], &[APP]),
        Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]),
    );
    s.nav = Nav::Crashes;
    s.show_crash_code("AppKit`-[NSView display] + 12", APP);
    let cancel = Arc::clone(&s.codeview.flight.as_ref().unwrap().1);
    assert!(!s.land_codeview());
    s.nav = Nav::Timeline;
    assert!(s.land_codeview());
    assert!(s.codeview.open.is_none());
    assert!(cancel.load(Ordering::SeqCst));
    let l = s
        .codeview
        .rx
        .recv_timeout(Duration::from_secs(5))
        .expect("the job ended");
    assert!(l.answer.is_none());
}

#[test]
fn a_second_press_replaces_the_first() {
    let mut r = FakeRunner::new(&[]);
    r.block = Some("lldb".into());
    let (mut s, _) = app_with(
        r,
        FakeProbe::new(&[], &[APP]),
        Tools::with(&[(Tool::Lldb, "/usr/bin/lldb")]),
    );
    s.show_crash_code("AppKit`-[NSView display] + 12", APP);
    let first = Arc::clone(&s.codeview.flight.as_ref().unwrap().1);
    s.show_crash_code("???", APP);
    assert!(first.load(Ordering::SeqCst));
    let start = Instant::now();
    while s.codeview.flight.is_some() && start.elapsed() < Duration::from_secs(5) {
        s.land_codeview();
        std::thread::sleep(Duration::from_millis(5));
    }
    let p = s.codeview.open.as_ref().unwrap();
    assert_eq!(p.title, "???");
    assert!(matches!(&p.state, State::Nothing(w) if w.contains("which binary")));
}

#[test]
fn the_real_runner_stops_at_its_deadline_and_cap() {
    let never = AtomicBool::new(false);
    let slow = System.run(
        Path::new("/bin/sleep"),
        &["5".into()],
        Duration::from_secs(1),
        CAP,
        &never,
    );
    assert_eq!(
        slow,
        Err(Failure::Failed(
            "sleep took longer than 1 s and was stopped".into()
        ))
    );
    let loud = System.run(
        Path::new("/usr/bin/yes"),
        &[],
        Duration::from_secs(10),
        64 * 1024,
        &never,
    );
    assert_eq!(
        loud,
        Err(Failure::Failed(
            "yes printed more than 64 KB and was stopped".into()
        ))
    );
    let failing = System.run(
        Path::new("/bin/ls"),
        &["/no/such/folder/here".into()],
        DEADLINE,
        CAP,
        &never,
    );
    assert!(matches!(failing, Err(Failure::Failed(w)) if w.starts_with("ls: ")));
    let cancelled = AtomicBool::new(true);
    assert_eq!(
        System.run(
            Path::new("/bin/sleep"),
            &["5".into()],
            DEADLINE,
            CAP,
            &cancelled
        ),
        Err(Failure::Cancelled)
    );
}

#[test]
fn temp_folders_are_made_in_the_temp_dir_and_removed() {
    let d = System.temp_dir().unwrap();
    assert_eq!(d.parent(), Some(std::env::temp_dir().as_path()));
    System.write(&d.join("out.c"), "int x;").unwrap();
    std::fs::create_dir(d.join("out")).unwrap();
    System
        .write(&d.join("out/Ledger.java"), "class Ledger {}")
        .unwrap();
    assert_eq!(System.find(&d, "java"), Some(d.join("out/Ledger.java")));
    assert_eq!(System.read(&d.join("out.c"), CAP).unwrap(), "int x;");
    assert!(System.read(&d.join("out.c"), 3).is_err());
    System.remove_temp(&d);
    assert!(!d.exists());
    // Anything not ours is left alone.
    System.remove_temp(Path::new("/"));
    System.remove_temp(&std::env::temp_dir());
    assert!(std::env::temp_dir().exists());
}

/// lldb disassembling a real libdispatch function from the shared cache, through a stock app.
/// Nothing from it is kept.
#[test]
#[ignore = "runs the real lldb"]
fn live_lldb_disassembles_a_libdispatch_function() {
    let cache = Mutex::new(None);
    let state = work(
        &Request::Crash {
            frame: "libdispatch.dylib`_dispatch_client_callout + 12".into(),
            process_path: String::new(),
        },
        &System,
        &SystemProbe,
        &cache,
        &AtomicBool::new(false),
    );
    let Some(State::Shown(sh)) = state else {
        panic!("{state:?}")
    };
    eprintln!(
        "{} · {} lines · mark {:?} · {}",
        sh.what,
        sh.lines.len(),
        sh.mark,
        sh.note
    );
    assert_eq!(sh.what, "Disassembly from lldb");
    assert!(sh.lines[0].contains("_dispatch_client_callout"));
    let m = sh.mark.expect("+12 is marked");
    assert!(sh.lines[m].contains("<+12>"));
}

/// The same for a Foundation method, found by its Objective-C name.
#[test]
#[ignore = "runs the real lldb"]
fn live_lldb_disassembles_a_foundation_method() {
    let state = work(
        &Request::Crash {
            frame: "Foundation`-[NSString stringByAppendingString:] + 16".into(),
            process_path: String::new(),
        },
        &System,
        &SystemProbe,
        &Mutex::new(None),
        &AtomicBool::new(false),
    );
    let Some(State::Shown(sh)) = state else {
        panic!("{state:?}")
    };
    eprintln!(
        "{} · {} lines · mark {:?}",
        sh.what,
        sh.lines.len(),
        sh.mark
    );
    assert!(sh.mark.is_some());
}

/// javap on a JDK class, from whichever JDK is on PATH.
#[test]
#[ignore = "runs the real javap"]
fn live_javap_shows_a_jdk_methods_bytecode() {
    let state = work(
        &Request::Java {
            frame: "java.base/java.lang.Thread.run(Thread.java:1583)".into(),
            command_line: String::new(),
            exe: String::new(),
        },
        &System,
        &SystemProbe,
        &Mutex::new(None),
        &AtomicBool::new(false),
    );
    let Some(State::Shown(sh)) = state else {
        panic!("{state:?}")
    };
    eprintln!(
        "{} · {} lines · mark {:?} · {}",
        sh.what,
        sh.lines.len(),
        sh.mark,
        sh.note
    );
    assert_eq!(sh.what, "Bytecode from javap");
}
