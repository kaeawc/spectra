//! What the code view runs for a frame. A press names a frame and where it came from (a
//! [`Request`]); off the UI thread that becomes a [`Target`] (which binary, class or file, and
//! the offset or line), and the target and the tools found become the [`Step`]s to try, best
//! first: a decompiler, then a disassembler or `javap`, then the source file. Nothing here runs
//! anything; `codeview.rs` does.

use std::path::{Path, PathBuf};

use crate::codeview_tools::{Probe, Tool, Tools};
use crate::crash_source::{parse_frame, search_term};

/// Where a "Show code" press came from.
#[derive(Clone, Debug, PartialEq)]
pub enum Request {
    /// A crash report's frame (`image`symbol + 20` or `image + 0x1a2c4`) and the crashed
    /// process's executable as the report names it.
    Crash { frame: String, process_path: String },
    /// A function a CPU sample caught running, in a process with this executable.
    Sampled { symbol: String, exe: String },
    /// A JVM thread dump's frame, with the JVM's command line and executable.
    Java {
        frame: String,
        command_line: String,
        exe: String,
    },
    /// A JavaScript or Python stack frame that names its file. No page lists such frames yet;
    /// the backend is here for the first that does.
    #[cfg_attr(not(test), allow(dead_code))]
    Script { frame: String },
}

impl Request {
    /// The panel's title: the frame as it was shown.
    pub fn title(&self) -> String {
        match self {
            Request::Crash { frame, .. }
            | Request::Java { frame, .. }
            | Request::Script { frame } => frame.trim().to_owned(),
            Request::Sampled { symbol, .. } => symbol.trim().to_owned(),
        }
    }
}

/// Native code: Mach-O, from any compiler.
#[derive(Clone, Debug, PartialEq)]
pub struct Native {
    /// The image as the frame names it; empty when the frame doesn't say.
    pub image: String,
    /// Names to look the function up by, best first.
    pub names: Vec<String>,
    /// Bytes into the function when there are names, else into the image.
    pub offset: Option<u64>,
    /// The image as a file, when it is one on disk.
    pub binary: Option<PathBuf>,
    /// What lldb loads (with what it links) to reach an image with no file of its own, such as
    /// one in the shared cache.
    pub host: Option<PathBuf>,
}

/// Where a class's bytes are.
#[derive(Clone, Debug, PartialEq)]
pub enum ClassAt {
    /// A `.class` file in a classpath folder.
    File(PathBuf),
    Jar(PathBuf),
}

#[derive(Clone, Debug, PartialEq)]
pub struct Jvm {
    /// Binary name, `com.acme.Ledger$Entry`.
    pub class: String,
    pub method: String,
    pub line: Option<u32>,
    /// The JDK's own class, read from its runtime image.
    pub jdk: bool,
    pub classpath: Vec<PathBuf>,
    pub at: Option<ClassAt>,
    /// The `javap` of the JDK the process runs, when it has one.
    pub javap: Option<PathBuf>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Lang {
    Js,
    Python,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Script {
    pub lang: Lang,
    /// The source file, when it's on disk.
    pub path: Option<PathBuf>,
    /// Python's compiled file, when there's no source.
    pub pyc: Option<PathBuf>,
    pub line: Option<u32>,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Target {
    Native(Native),
    Jvm(Jvm),
    Script(Script),
    /// No code view, and why.
    Nothing(String),
}

/// How a step's output is read.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Parse {
    Lldb,
    Objdump,
    Otool,
    /// Pseudo-C from a native decompiler.
    PseudoC,
    JavaDecompiled,
    Javap,
    /// A source file, with its line marked.
    Source,
    /// A minified file, pretty-printed.
    Pretty,
    PyDecompiled,
    PyDis,
}

impl Parse {
    /// What the panel says it is, with the tool's name.
    pub fn what(self, tool: &str) -> String {
        match self {
            Parse::Lldb | Parse::Objdump | Parse::Otool => format!("Disassembly from {tool}"),
            Parse::PseudoC => format!("Pseudo-C decompiled by {tool}"),
            Parse::JavaDecompiled => format!("Java decompiled by {tool}"),
            Parse::Javap => format!("Bytecode from {tool}"),
            Parse::Source => "Source file".into(),
            Parse::Pretty => format!("Source file, pretty-printed by {tool}"),
            Parse::PyDecompiled => format!("Python decompiled by {tool}"),
            Parse::PyDis => format!("Bytecode from {tool} -m dis"),
        }
    }
}

/// Where a step's answer is.
#[derive(Clone, Debug, PartialEq)]
pub enum Output {
    Stdout,
    /// A file it writes in the step's temp folder.
    File(String),
    /// The first `.java` file under a folder in the step's temp folder.
    JavaIn(String),
    /// A file read without running anything.
    Read(PathBuf),
}

/// Stands for the step's own temp folder in its arguments.
pub const TMP: &str = "{tmp}";

/// One thing to try.
#[derive(Clone, Debug, PartialEq)]
pub struct Step {
    /// The tool it runs; none when it only reads a file.
    pub tool: Option<Tool>,
    pub program: PathBuf,
    pub args: Vec<String>,
    /// Files written into the temp folder first: name and contents.
    pub files: Vec<(String, String)>,
    /// Folders made in the temp folder first.
    pub dirs: Vec<String>,
    pub output: Output,
    pub parse: Parse,
    /// Run only when the source file is minified.
    pub only_if_minified: bool,
}

impl Step {
    fn run(tool: Tool, program: &Path, args: Vec<String>, parse: Parse) -> Step {
        Step {
            tool: Some(tool),
            program: program.to_path_buf(),
            args,
            files: Vec::new(),
            dirs: Vec::new(),
            output: Output::Stdout,
            parse,
            only_if_minified: false,
        }
    }

    /// Whether it needs a temp folder.
    pub fn needs_temp(&self) -> bool {
        self.args.iter().any(|a| a.contains(TMP))
            || matches!(self.output, Output::File(_) | Output::JavaIn(_))
    }

    /// What it's called on the page.
    pub fn label(&self) -> &'static str {
        self.tool.map_or("the file", Tool::label)
    }
}

// Requests to targets.

/// `Swift.Int.+ infix(_:_:)`, `hir_ty::infer::InferenceContext::infer_body::h3b1f0c9a2d4e5f60`:
/// a Rust symbol's hash goes; Go's and the rest stay as they read.
pub fn clean_symbol(s: &str) -> String {
    strip_rust_hashes(s.trim())
}

/// Removes each `::h` + 16 hex digits.
pub fn strip_rust_hashes(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    while i < s.len() {
        if b[i..].starts_with(b"::h")
            && b.len() >= i + 19
            && b[i + 3..i + 19].iter().all(u8::is_ascii_hexdigit)
            && b.get(i + 19).is_none_or(|c| !c.is_ascii_alphanumeric())
        {
            i += 19;
            continue;
        }
        let ch = s[i..].chars().next().unwrap_or(' ');
        out.push(ch);
        i += ch.len_utf8();
    }
    out
}

/// Whether a name is a placeholder rather than a symbol.
fn placeholder(s: &str) -> bool {
    let s = s.trim();
    s.is_empty() || s.contains("???") || s == "<redacted>" || s.starts_with("0x")
}

/// The names lldb tries: the symbol as cleaned, then its own name alone.
fn names_for(symbol: &str) -> Vec<String> {
    let full = clean_symbol(symbol);
    let mut v = vec![full.clone()];
    if let Some(base) = search_term(&full).filter(|b| *b != full) {
        v.push(base);
    }
    v
}

/// `+ 20` (into a symbol) or `+ 0x1a2c4` (into an image) at the end of a frame.
fn trailing_offset(frame: &str) -> Option<u64> {
    let (_, off) = frame.trim().rsplit_once(" + ")?;
    match off.strip_prefix("0x") {
        Some(hex) => u64::from_str_radix(hex, 16).ok(),
        None => off.parse().ok(),
    }
}

/// An on-disk binary lldb can load to reach shared-cache images: the process itself when it's
/// still there, else a stock app that links most of the system.
fn cache_host(process_path: &str, p: &dyn Probe) -> Option<PathBuf> {
    let candidates = [
        process_path,
        "/System/Applications/TextEdit.app/Contents/MacOS/TextEdit",
        "/bin/ls",
    ];
    candidates
        .iter()
        .filter(|c| !c.is_empty())
        .map(PathBuf::from)
        .find(|c| p.is_file(c))
}

/// The image as a file on disk: the process itself, a framework or dylib in its bundle or
/// beside it, or a Homebrew library. Shared-cache images have no file and get nothing.
pub fn locate_image(image: &str, process_path: &str, p: &dyn Probe) -> Option<PathBuf> {
    let image = image.trim();
    if image.is_empty() || image.contains('/') {
        return None;
    }
    let exe = Path::new(process_path);
    let mut c: Vec<PathBuf> = Vec::new();
    if exe.file_name().is_some_and(|n| n == image) {
        c.push(exe.to_path_buf());
    }
    if let Some(i) = process_path.find(".app/Contents/") {
        let contents = Path::new(&process_path[..i + ".app/Contents".len()]);
        let fw = contents.join("Frameworks");
        c.push(fw.join(format!("{image}.framework/{image}")));
        c.push(fw.join(format!("{image}.framework/Versions/A/{image}")));
        c.push(fw.join(image));
    }
    if let Some(dir) = exe.parent().filter(|d| !d.as_os_str().is_empty()) {
        c.push(dir.join(image));
    }
    for lib in ["/opt/homebrew/lib", "/usr/local/lib"] {
        c.push(Path::new(lib).join(image));
    }
    for sys in ["/usr/lib/system", "/usr/lib"] {
        c.push(Path::new(sys).join(image));
    }
    for fw in [
        "/System/Library/Frameworks",
        "/System/Library/PrivateFrameworks",
    ] {
        c.push(Path::new(fw).join(format!("{image}.framework/{image}")));
    }
    c.into_iter().find(|f| p.is_file(f))
}

fn crash_target(frame: &str, process_path: &str, p: &dyn Probe) -> Target {
    let f = parse_frame(frame);
    if placeholder(f.image) {
        return Target::Nothing("The report doesn't say which binary this frame is in.".into());
    }
    let symbol = f.symbol.filter(|s| !placeholder(s));
    let offset = trailing_offset(frame);
    // A symbol's offset is decimal; an image's is hex. Anything else isn't a usable offset.
    let offset = match (symbol, frame.trim().rsplit_once(" + ")) {
        (Some(_), Some((_, o))) if !o.starts_with("0x") => offset,
        (None, Some((_, o))) if o.starts_with("0x") => offset,
        _ => None,
    };
    if symbol.is_none() && offset.is_none() {
        return Target::Nothing(
            "The report gives no symbol or offset for this frame, so there's nothing to look up."
                .into(),
        );
    }
    let binary = locate_image(f.image, process_path, p);
    let host = if binary.is_none() {
        cache_host(process_path, p)
    } else {
        None
    };
    Target::Native(Native {
        image: f.image.to_owned(),
        names: symbol.map(names_for).unwrap_or_default(),
        offset,
        binary,
        host,
    })
}

fn sampled_target(symbol: &str, exe: &str, p: &dyn Probe) -> Target {
    if placeholder(symbol) || crate::flame::unnamed(symbol) {
        return Target::Nothing(
            "The sample has no name for this function, so there's nothing to look up.".into(),
        );
    }
    if exe.is_empty() || !p.is_file(Path::new(exe)) {
        return Target::Nothing(
            "Couldn't find the process's executable on disk to look the function up in.".into(),
        );
    }
    Target::Native(Native {
        image: String::new(),
        names: names_for(symbol),
        offset: None,
        binary: None,
        host: Some(PathBuf::from(exe)),
    })
}

/// A Java frame, split.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct JavaFrame {
    pub class: String,
    pub method: String,
    pub file: Option<String>,
    pub line: Option<u32>,
    /// `java.base@21.0.5`, `app`.
    pub module: Option<String>,
    pub native: bool,
}

/// `com.acme.Ledger.reserve(Ledger.java:212)`, `java.base@21/java.lang.Thread.run(Thread.java:1583)`,
/// `jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)`, with or without `at `.
pub fn parse_java(frame: &str) -> Option<JavaFrame> {
    let f = frame.trim();
    let f = f.strip_prefix("at ").unwrap_or(f).trim();
    let (head, tail) = f.split_once('(')?;
    let tail = tail.strip_suffix(')').unwrap_or(tail);
    let mut module = None;
    let qualified = match head.rsplit_once('/') {
        Some((m, q)) => {
            let m = m.trim_end_matches('/');
            if !m.is_empty() {
                module = Some(m.to_owned());
            }
            q
        }
        None => head,
    };
    let (class, method) = qualified.rsplit_once('.')?;
    let ok = |s: &str| {
        !s.is_empty()
            && s.chars()
                .all(|c| c.is_alphanumeric() || matches!(c, '_' | '$' | '.' | '<' | '>'))
    };
    if !ok(class) || !ok(method) {
        return None;
    }
    let (m2, place) = match tail.rsplit_once('/') {
        Some((m, rest)) => (Some(m.to_owned()), rest),
        None => (None, tail),
    };
    if module.is_none() {
        module = m2;
    }
    let native = place.trim() == "Native Method";
    let (file, line) = match place.rsplit_once(':') {
        Some((file, n)) => (Some(file.to_owned()), n.trim().parse().ok()),
        None if !native && place != "Unknown Source" && !place.is_empty() => {
            (Some(place.to_owned()), None)
        }
        None => (None, None),
    };
    Some(JavaFrame {
        class: class.to_owned(),
        method: method.to_owned(),
        file,
        line,
        module,
        native,
    })
}

/// Whether a class is the JDK's own.
fn jdk_class(f: &JavaFrame) -> bool {
    let m = f.module.as_deref().unwrap_or("");
    m.starts_with("java.")
        || m.starts_with("jdk.")
        || ["java.", "javax.", "jdk.", "sun.", "com.sun."]
            .iter()
            .any(|p| f.class.starts_with(p))
}

/// The classpath a JVM's command line names: `-cp`, `-classpath`, `--class-path` and `-jar`.
/// Relative entries are left out (the JVM's folder isn't known) and `dir/*` is the jars in it.
/// A path with spaces in it can't be told apart from the next argument and is lost.
pub fn classpath(command_line: &str, p: &dyn Probe) -> Vec<PathBuf> {
    let words: Vec<&str> = command_line.split_whitespace().collect();
    let mut raw: Vec<&str> = Vec::new();
    let mut i = 0;
    while i < words.len() {
        let w = words[i];
        if let Some(v) = w.strip_prefix("--class-path=") {
            raw.extend(v.split(':'));
        } else if matches!(w, "-cp" | "-classpath" | "--class-path") {
            if let Some(v) = words.get(i + 1) {
                raw.extend(v.split(':'));
                i += 1;
            }
        } else if w == "-jar" {
            if let Some(v) = words.get(i + 1) {
                raw.push(v);
            }
            // What follows the jar is the program's own arguments.
            break;
        }
        i += 1;
    }
    let mut out = Vec::new();
    for e in raw {
        if !e.starts_with('/') {
            continue;
        }
        match e.strip_suffix("/*") {
            Some(dir) => out.extend(
                p.list(Path::new(dir))
                    .into_iter()
                    .filter(|j| j.extension().is_some_and(|x| x == "jar")),
            ),
            None => out.push(PathBuf::from(e)),
        }
    }
    out
}

/// Where JDKs install, links resolved: the system's and the user's JDK folders, Homebrew's
/// OpenJDKs, SDKMAN's, asdf's and jenv's.
fn jdk_roots(p: &dyn Probe) -> Vec<PathBuf> {
    let mut raw = vec![PathBuf::from("/Library/Java/JavaVirtualMachines")];
    if let Some(h) = p.home() {
        for d in [
            "Library/Java/JavaVirtualMachines",
            ".sdkman/candidates/java",
            ".asdf/installs/java",
            ".jenv",
        ] {
            raw.push(h.join(d));
        }
    }
    for opt in ["/opt/homebrew/opt", "/usr/local/opt"] {
        raw.extend(p.list(Path::new(opt)).into_iter().filter(|e| {
            e.file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| n.contains("openjdk"))
        }));
    }
    raw.iter().filter_map(|r| p.canonical(r)).collect()
}

/// The `javap` beside the JVM's executable. The path comes from the process, so it's trusted
/// only inside a JDK's install folder, owned by this user or root, with neither it nor its
/// folder writable by anyone else. Otherwise detection's `javap` is used.
fn own_javap(exe: &str, p: &dyn Probe) -> Option<PathBuf> {
    if exe.is_empty() {
        return None;
    }
    let beside = Path::new(exe).parent()?.join("javap");
    if !p.is_file(&beside) {
        return None;
    }
    let real = p.canonical(&beside)?;
    let inside = |roots: &[PathBuf]| roots.iter().any(|r| real.starts_with(r));
    if !inside(&jdk_roots(p))
        && !inside(
            &p.java_home()
                .and_then(|h| p.canonical(&h))
                .into_iter()
                .collect::<Vec<_>>(),
        )
    {
        return None;
    }
    let uid = p.uid();
    let safe = |f: &Path| {
        p.owner_mode(f)
            .is_some_and(|(owner, mode)| (owner == uid || owner == 0) && mode & 0o022 == 0)
    };
    (safe(&real) && real.parent().is_some_and(safe)).then_some(real)
}

fn java_target(
    frame: &str,
    command_line: &str,
    exe: &str,
    p: &dyn Probe,
    stop: &dyn Fn() -> bool,
) -> Target {
    let Some(f) = parse_java(frame) else {
        return Target::Nothing(
            "This doesn't read as a Java frame (class.method(File.java:line)).".into(),
        );
    };
    if f.native {
        return Target::Nothing(format!(
            "{} is a native method: it has no bytecode. Its code is in the JVM or a native library.",
            f.method
        ));
    }
    let jdk = jdk_class(&f);
    let cp = if jdk {
        Vec::new()
    } else {
        classpath(command_line, p)
    };
    let entry = format!("{}.class", f.class.replace('.', "/"));
    let mut at = None;
    for e in &cp {
        if stop() {
            break;
        }
        let is_jar = e.extension().is_some_and(|x| x == "jar");
        let found = if is_jar {
            p.jar_has(e, &entry).then(|| ClassAt::Jar(e.clone()))
        } else {
            let file = e.join(&entry);
            p.is_file(&file).then_some(ClassAt::File(file))
        };
        if found.is_some() {
            at = found;
            break;
        }
    }
    let javap = if stop() { None } else { own_javap(exe, p) };
    Target::Jvm(Jvm {
        class: f.class,
        method: f.method,
        line: f.line,
        jdk,
        classpath: cp,
        at,
        javap,
    })
}

/// `at fn (/srv/app/x.js:12:5)`, `at /srv/app/x.js:12:5`, `file:///srv/app/x.mjs:3:1`,
/// `File "/srv/app/x.py", line 12, in run`, or `/srv/app/x.py:12`: the path and line.
pub fn parse_script(frame: &str) -> Option<(String, Option<u32>)> {
    let f = frame.trim();
    if let Some(rest) = f.strip_prefix("File \"") {
        let (path, after) = rest.split_once('"')?;
        let line = after
            .split("line ")
            .nth(1)
            .and_then(|l| l.split(|c: char| !c.is_ascii_digit()).next())
            .and_then(|n| n.parse().ok());
        return Some((path.to_owned(), line));
    }
    let inner = match (f.rfind('('), f.ends_with(')')) {
        (Some(i), true) => &f[i + 1..f.len() - 1],
        _ => f.strip_prefix("at ").unwrap_or(f),
    };
    let inner = inner.strip_prefix("file://").unwrap_or(inner);
    // Up to two trailing `:number`s: the line, then the column.
    let mut path = inner;
    let mut nums = Vec::new();
    for _ in 0..2 {
        match path.rsplit_once(':') {
            Some((p, n)) if !n.is_empty() && n.bytes().all(|b| b.is_ascii_digit()) => {
                nums.push(n.parse::<u32>().ok()?);
                path = p;
            }
            _ => break,
        }
    }
    let line = match nums.len() {
        2 => Some(nums[1]),
        1 => Some(nums[0]),
        _ => None,
    };
    (!path.is_empty()).then(|| (path.to_owned(), line))
}

fn script_target(frame: &str, p: &dyn Probe) -> Target {
    let Some((path, line)) = parse_script(frame) else {
        return Target::Nothing("This frame doesn't name a file.".into());
    };
    if path.starts_with("node:") || path.starts_with("internal/") {
        return Target::Nothing(format!(
            "{path} is one of Node's built-in modules: it's compiled into node and has no file here."
        ));
    }
    let file = Path::new(&path);
    let ext = file.extension().and_then(|e| e.to_str()).unwrap_or("");
    let lang = match ext {
        "py" | "pyc" | "pyw" => Lang::Python,
        "js" | "mjs" | "cjs" | "jsx" | "ts" | "tsx" => Lang::Js,
        _ => {
            return Target::Nothing(format!(
                "There's no code view for .{ext} files, only JavaScript and Python."
            ));
        }
    };
    if !path.starts_with('/') {
        return Target::Nothing(format!(
            "{path} is a relative path, and the folder it's relative to isn't known."
        ));
    }
    if ext == "pyc" && p.is_file(file) {
        return Target::Script(Script {
            lang,
            path: None,
            pyc: Some(file.to_path_buf()),
            line,
        });
    }
    if p.is_file(file) {
        return Target::Script(Script {
            lang,
            path: Some(file.to_path_buf()),
            pyc: None,
            line,
        });
    }
    // Python keeps `__pycache__/x.cpython-312.pyc` beside where the source was.
    if lang == Lang::Python
        && let (Some(dir), Some(stem)) = (file.parent(), file.file_stem().and_then(|s| s.to_str()))
        && let Some(pyc) = p
            .list(&dir.join("__pycache__"))
            .into_iter()
            .rev()
            .find(|c| {
                c.file_name()
                    .and_then(|n| n.to_str())
                    .is_some_and(|n| n.starts_with(&format!("{stem}.")) && n.ends_with(".pyc"))
            })
    {
        return Target::Script(Script {
            lang,
            path: None,
            pyc: Some(pyc),
            line,
        });
    }
    Target::Nothing(format!("{path} isn't on this Mac."))
}

/// What a request points at. Reads only file metadata (and a jar's directory).
#[cfg(test)]
pub fn resolve(req: &Request, p: &dyn Probe) -> Target {
    resolve_until(req, p, &|| false)
}

/// [`resolve`], giving up between steps once `stop` says so (cancelled or out of time).
pub fn resolve_until(req: &Request, p: &dyn Probe, stop: &dyn Fn() -> bool) -> Target {
    if stop() {
        return Target::Nothing("Stopped before the frame was looked up.".into());
    }
    match req {
        Request::Crash {
            frame,
            process_path,
        } => crash_target(frame, process_path, p),
        Request::Sampled { symbol, exe } => sampled_target(symbol, exe, p),
        Request::Java {
            frame,
            command_line,
            exe,
        } => java_target(frame, command_line, exe, p, stop),
        Request::Script { frame } => script_target(frame, p),
    }
}

// Targets to steps.

/// `s` as a Python string literal.
pub fn py_str(s: &str) -> String {
    let mut out = String::from("'");
    for c in s.chars() {
        match c {
            '\\' => out.push_str("\\\\"),
            '\'' => out.push_str("\\'"),
            c if (' '..='~').contains(&c) => out.push(c),
            c => out.push_str(&format!("\\U{:08x}", c as u32)),
        }
    }
    out.push('\'');
    out
}

fn hex(s: &str) -> String {
    s.bytes().map(|b| format!("{b:02x}")).collect()
}

/// The lldb script: load `file` (with what it links, or alone), find the function by `names`
/// in `image` (any image when empty), and disassemble up to four matches; with no names,
/// disassemble the function at `offset` into `image`. It prints `CODEVIEW_AT=<address>` for
/// the offset's instruction, or `CODEVIEW_NONE` when nothing matched. Names and paths are
/// Python literals, and the whole script goes in hex, so no character in them reaches lldb's
/// command parser.
pub fn lldb_script(
    file: &Path,
    deps: bool,
    image: &str,
    names: &[String],
    offset: Option<u64>,
) -> String {
    let names = names
        .iter()
        .map(|n| py_str(n))
        .collect::<Vec<_>>()
        .join(", ");
    let off = offset.map_or("None".to_owned(), |o| o.to_string());
    format!(
        r#"import lldb
err = lldb.SBError()
t = lldb.debugger.CreateTarget({file}, None, None, {deps}, err)
img = {img}
names = [{names}]
off = {off}
def mine(m):
    return not img or m.GetFileSpec().GetFilename() == img
if not t.IsValid():
    print('CODEVIEW_ERR=' + str(err.GetCString()))
elif not names:
    ms = [m for m in t.module_iter() if mine(m)]
    if ms and off is not None:
        a = ms[0].GetObjectFileHeaderAddress().GetFileAddress() + off
        print('CODEVIEW_AT=0x%x' % a)
        lldb.debugger.HandleCommand('disassemble -a 0x%x' % a)
    else:
        print('CODEVIEW_NONE')
else:
    seen = []
    for n in names:
        if seen:
            break
        scs = t.FindFunctions(n)
        if scs.GetSize() == 0:
            scs = t.FindSymbols(n)
        for sc in scs:
            if not mine(sc.GetModule()):
                continue
            f = sc.GetFunction()
            o = f if f.IsValid() else sc.GetSymbol()
            s = o.GetStartAddress().GetFileAddress()
            e = o.GetEndAddress().GetFileAddress()
            if s in seen or e <= s:
                continue
            seen.append(s)
            if off is not None and len(seen) == 1:
                print('CODEVIEW_AT=0x%x' % (s + off))
            lldb.debugger.HandleCommand('disassemble -s 0x%x -e 0x%x' % (s, min(e, s + 65536)))
            if len(seen) >= 4:
                break
    if not seen:
        print('CODEVIEW_NONE')
"#,
        file = py_str(&file.to_string_lossy()),
        deps = if deps { "True" } else { "False" },
        img = py_str(image),
    )
}

/// lldb's arguments for [`lldb_script`].
pub fn lldb_args(script: &str) -> Vec<String> {
    vec![
        "--batch".into(),
        "--no-lldbinit".into(),
        "-o".into(),
        format!("script exec(bytes.fromhex('{}').decode())", hex(script)),
    ]
}

/// A plain C identifier, safe to name inside another tool's command language.
fn c_ident(s: &str) -> bool {
    !s.is_empty()
        && s.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
        && !s.starts_with(|c: char| c.is_ascii_digit())
}

/// An Objective-C method's symbol, `-[Class selector:]`.
fn objc(s: &str) -> bool {
    (s.starts_with("-[") || s.starts_with("+[")) && s.ends_with(']') && !s.contains(',')
}

/// The symbol table's spellings of a name: C's leading underscore, or Objective-C's as is.
fn mach_o_names(name: &str) -> Vec<String> {
    if objc(name) {
        vec![name.to_owned()]
    } else if c_ident(name) {
        vec![format!("_{name}"), name.to_owned()]
    } else {
        Vec::new()
    }
}

/// The Ghidra script that writes one function's decompiled C to a file: `sym:<name>` finds it
/// by symbol (with or without C's underscore).
pub const GHIDRA_SCRIPT: &str = r#"// Writes one function's decompiled C to the file named by the second argument.
import ghidra.app.script.GhidraScript;
import ghidra.app.decompiler.DecompInterface;
import ghidra.app.decompiler.DecompileResults;
import ghidra.program.model.address.Address;
import ghidra.program.model.listing.Function;
import ghidra.program.model.symbol.Symbol;
import java.io.PrintWriter;

public class CodeViewDecompile extends GhidraScript {
    @Override
    public void run() throws Exception {
        String[] a = getScriptArgs();
        String name = a[0].substring(4);
        Address at = null;
        for (String n : new String[] {name, "_" + name}) {
            for (Symbol s : currentProgram.getSymbolTable().getSymbols(n)) {
                at = s.getAddress();
                break;
            }
            if (at != null) break;
        }
        if (at == null) throw new Exception("no function named " + name);
        Function f = getFunctionAt(at);
        if (f == null) {
            disassemble(at);
            f = createFunction(at, null);
        }
        if (f == null) throw new Exception("couldn't make a function at " + at);
        DecompInterface d = new DecompInterface();
        d.openProgram(currentProgram);
        DecompileResults r = d.decompileFunction(f, 20, monitor);
        if (!r.decompileCompleted()) throw new Exception(r.getErrorMessage());
        try (PrintWriter w = new PrintWriter(a[1], "UTF-8")) {
            w.print(r.getDecompiledFunction().getC());
        }
    }
}
"#;

/// Why there's no code view: the reason, and whether a tool would have helped (so detection
/// is worth running again).
#[derive(Clone, Debug, PartialEq)]
pub struct NoView {
    pub reason: String,
    pub missing_tool: bool,
}

fn no_tool(reason: impl Into<String>) -> NoView {
    NoView {
        reason: reason.into(),
        missing_tool: true,
    }
}

fn native_steps(n: &Native, tools: &Tools) -> Result<Vec<Step>, NoView> {
    let mut steps = Vec::new();
    let s = |v: &[&str]| v.iter().map(|x| (*x).to_owned()).collect::<Vec<String>>();
    let first = n.names.first();
    // Decompilers read a file: only for an image on disk, found by its symbol.
    if let (Some(bin), Some(name)) = (&n.binary, first) {
        let bin_s = bin.to_string_lossy().into_owned();
        if let Some(g) = tools.get(Tool::Ghidra)
            && (c_ident(name) || objc(name))
        {
            let mut st = Step::run(
                Tool::Ghidra,
                g,
                vec![
                    TMP.into(),
                    "codeview".into(),
                    "-import".into(),
                    bin_s.clone(),
                    "-noanalysis".into(),
                    "-scriptPath".into(),
                    TMP.into(),
                    "-postScript".into(),
                    "CodeViewDecompile.java".into(),
                    format!("sym:{name}"),
                    format!("{TMP}/out.c"),
                    "-deleteProject".into(),
                ],
                Parse::PseudoC,
            );
            st.files = vec![("CodeViewDecompile.java".into(), GHIDRA_SCRIPT.into())];
            st.output = Output::File("out.c".into());
            steps.push(st);
        }
        if let Some(r2) = tools.get(Tool::R2)
            && c_ident(name)
        {
            // r2ghidra's pdg, then r2dec's pdd; a missing plugin's complaint reads as no code.
            for cmd in ["pdg", "pdd"] {
                steps.push(Step::run(
                    Tool::R2,
                    r2,
                    vec![
                        "-q".into(),
                        "-e".into(),
                        "scr.color=0".into(),
                        "-e".into(),
                        "bin.relocs.apply=true".into(),
                        "-c".into(),
                        format!("aa; s sym._{name}; af; {cmd}"),
                        bin_s.clone(),
                    ],
                    Parse::PseudoC,
                ));
            }
        }
        if let Some(rd) = tools.get(Tool::RetDec)
            && c_ident(name)
        {
            let mut st = Step::run(
                Tool::RetDec,
                rd,
                vec![
                    "--select-functions".into(),
                    name.clone(),
                    "--cleanup".into(),
                    "-o".into(),
                    format!("{TMP}/out.c"),
                    bin_s.clone(),
                ],
                Parse::PseudoC,
            );
            st.output = Output::File("out.c".into());
            steps.push(st);
        }
    }
    if let Some(lldb) = tools.get(Tool::Lldb) {
        let (file, deps) = match (&n.binary, &n.host) {
            (Some(b), _) => (b, false),
            (None, Some(h)) => (h, true),
            (None, None) => {
                return Err(NoView {
                    reason: "Couldn't find a binary on disk to load this frame's image from."
                        .into(),
                    missing_tool: false,
                });
            }
        };
        let script = lldb_script(file, deps, &n.image, &n.names, n.offset);
        steps.push(Step::run(Tool::Lldb, lldb, lldb_args(&script), Parse::Lldb));
    }
    // Without lldb, a file on disk can still be disassembled by symbol.
    let on_disk = n
        .binary
        .as_ref()
        .or(n.host.as_ref().filter(|_| n.image.is_empty()));
    // Only the full name can be a symbol table's; its last part alone is lldb's fallback.
    let spellings: Vec<String> = n.names.first().map(|x| mach_o_names(x)).unwrap_or_default();
    if let Some(file) = on_disk.filter(|_| !spellings.is_empty()) {
        let f = file.to_string_lossy().into_owned();
        if let Some(od) = tools.get(Tool::Objdump) {
            let mut a = s(&["--no-show-raw-insn", "-C"]);
            a.push(format!("--disassemble-symbols={}", spellings.join(",")));
            a.push(f.clone());
            steps.push(Step::run(Tool::Objdump, od, a, Parse::Objdump));
        } else if let Some(ot) = tools.get(Tool::Otool) {
            steps.push(Step::run(
                Tool::Otool,
                ot,
                vec!["-tV".into(), "-p".into(), spellings[0].clone(), f],
                Parse::Otool,
            ));
        }
    }
    if !steps.is_empty() {
        return Ok(steps);
    }
    Err(if n.names.is_empty() {
        no_tool(
            "The report has no symbol for this frame; showing it by its offset needs lldb, which comes with Xcode's command line tools (xcode-select --install).",
        )
    } else if n.binary.is_none() && !n.image.is_empty() {
        no_tool(format!(
            "{} has no file on disk (it's in macOS's shared cache), and only lldb reads code there. lldb comes with Xcode's command line tools (xcode-select --install).",
            n.image
        ))
    } else if spellings.is_empty() && (tools.has(Tool::Objdump) || tools.has(Tool::Otool)) {
        no_tool(
            "Only lldb can find a Swift, C++ or Rust function by this name; it comes with Xcode's command line tools (xcode-select --install).",
        )
    } else {
        no_tool(
            "No disassembler found. lldb, llvm-objdump and otool come with Xcode's command line tools (xcode-select --install).",
        )
    })
}

/// `com.acme.Ledger$Entry` as a CFR class filter.
fn class_regex(class: &str) -> String {
    let mut r = String::from("^");
    for c in class.chars() {
        if c == '.' || c == '$' {
            r.push('\\');
        }
        r.push(c);
    }
    r.push('$');
    r
}

fn jvm_steps(j: &Jvm, tools: &Tools) -> Result<Vec<Step>, NoView> {
    let mut steps = Vec::new();
    let java = tools.get(Tool::Java);
    let jar_step = |tool: Tool, jar: &Path, main: Option<&str>, rest: Vec<String>, parse| {
        let mut a = match main {
            Some(m) => vec!["-cp".into(), jar.to_string_lossy().into_owned(), m.into()],
            None => vec!["-jar".into(), jar.to_string_lossy().into_owned()],
        };
        a.extend(rest);
        java.map(|java| {
            let mut st = Step::run(tool, java, a, parse);
            st.tool = Some(tool);
            st
        })
    };
    if let Some(at) = &j.at {
        let (input, jar) = match at {
            ClassAt::File(f) => (f.to_string_lossy().into_owned(), false),
            ClassAt::Jar(f) => (f.to_string_lossy().into_owned(), true),
        };
        let mut cfr_args = vec![input.clone()];
        if jar {
            cfr_args.extend(["--jarfilter".into(), class_regex(&j.class)]);
        }
        if let Some(c) = tools.get(Tool::Cfr) {
            steps.push(Step::run(
                Tool::Cfr,
                c,
                cfr_args.clone(),
                Parse::JavaDecompiled,
            ));
        } else if let Some(jar) = tools.get(Tool::CfrJar)
            && let Some(st) = jar_step(Tool::CfrJar, jar, None, cfr_args, Parse::JavaDecompiled)
        {
            steps.push(st);
        }
        // Procyon and Fernflower take the class file; on a jar they'd decompile all of it.
        if !jar {
            if let Some(p) = tools.get(Tool::Procyon) {
                steps.push(Step::run(
                    Tool::Procyon,
                    p,
                    vec![input.clone()],
                    Parse::JavaDecompiled,
                ));
            } else if let Some(jar) = tools.get(Tool::ProcyonJar)
                && let Some(st) = jar_step(
                    Tool::ProcyonJar,
                    jar,
                    None,
                    vec![input.clone()],
                    Parse::JavaDecompiled,
                )
            {
                steps.push(st);
            }
            if let Some(jar) = tools.get(Tool::FernflowerJar)
                && let Some(mut st) = jar_step(
                    Tool::FernflowerJar,
                    jar,
                    Some("org.jetbrains.java.decompiler.main.decompiler.ConsoleDecompiler"),
                    vec![input.clone(), format!("{TMP}/out")],
                    Parse::JavaDecompiled,
                )
            {
                // Fernflower writes into a folder that must already be there.
                st.dirs = vec!["out".into()];
                st.output = Output::JavaIn("out".into());
                steps.push(st);
            }
        }
        if let Some(jx) = tools.get(Tool::Jadx) {
            let mut st = Step::run(
                Tool::Jadx,
                jx,
                vec![
                    "--single-class".into(),
                    j.class.clone(),
                    "-d".into(),
                    format!("{TMP}/out"),
                    input,
                ],
                Parse::JavaDecompiled,
            );
            st.output = Output::JavaIn("out".into());
            steps.push(st);
        }
    }
    let javap = j.javap.as_deref().or(tools.get(Tool::Javap));
    if let Some(javap) = javap {
        let mut a = vec!["-c".into(), "-p".into(), "-l".into()];
        if !j.jdk && !j.classpath.is_empty() {
            a.push("-cp".into());
            a.push(
                j.classpath
                    .iter()
                    .map(|e| e.to_string_lossy().into_owned())
                    .collect::<Vec<_>>()
                    .join(":"),
            );
        }
        a.push(j.class.clone());
        steps.push(Step::run(Tool::Javap, javap, a, Parse::Javap));
    }
    if steps.is_empty() {
        return Err(no_tool(
            "No javap or Java decompiler found. A JDK's javap shows the bytecode; CFR, Procyon, Fernflower or jadx show Java.",
        ));
    }
    Ok(steps)
}

/// Prints a `.pyc`'s bytecode: skip the 16-byte header, unmarshal, disassemble.
pub const PY_DIS: &str =
    "import dis, marshal, sys\nf = open(sys.argv[1], 'rb')\nf.read(16)\ndis.dis(marshal.load(f))\n";

fn script_steps(sc: &Script, tools: &Tools) -> Result<Vec<Step>, NoView> {
    let mut steps = Vec::new();
    if let Some(path) = &sc.path {
        let p = path.to_string_lossy().into_owned();
        if sc.lang == Lang::Js {
            let fmt = match tools.first(&[Tool::Prettier, Tool::JsBeautify]) {
                Some((Tool::Prettier, b)) => Some(Step::run(
                    Tool::Prettier,
                    b,
                    vec!["--parser".into(), "babel".into(), p.clone()],
                    Parse::Pretty,
                )),
                Some((t, b)) => Some(Step::run(t, b, vec![p.clone()], Parse::Pretty)),
                None => None,
            };
            if let Some(mut f) = fmt {
                f.only_if_minified = true;
                steps.push(f);
            }
        }
        steps.push(Step {
            tool: None,
            program: PathBuf::new(),
            args: Vec::new(),
            files: Vec::new(),
            dirs: Vec::new(),
            output: Output::Read(path.clone()),
            parse: Parse::Source,
            only_if_minified: false,
        });
        return Ok(steps);
    }
    if let Some(pyc) = &sc.pyc {
        let p = pyc.to_string_lossy().into_owned();
        if let Some(d) = tools.get(Tool::Pycdc) {
            steps.push(Step::run(
                Tool::Pycdc,
                d,
                vec![p.clone()],
                Parse::PyDecompiled,
            ));
        }
        if let Some(py) = tools.get(Tool::Python) {
            steps.push(Step::run(
                Tool::Python,
                py,
                vec!["-I".into(), "-c".into(), PY_DIS.into(), p],
                Parse::PyDis,
            ));
        }
        if steps.is_empty() {
            return Err(no_tool(
                "Only the compiled .pyc is here, and there's no python3 or pycdc to read it.",
            ));
        }
        return Ok(steps);
    }
    Err(NoView {
        reason: "The file isn't on this Mac.".into(),
        missing_tool: false,
    })
}

/// The steps to try, best first, or why there's nothing to try.
pub fn plan(t: &Target, tools: &Tools) -> Result<Vec<Step>, NoView> {
    match t {
        Target::Native(n) => native_steps(n, tools),
        Target::Jvm(j) => jvm_steps(j, tools),
        Target::Script(s) => script_steps(s, tools),
        Target::Nothing(why) => Err(NoView {
            reason: why.clone(),
            missing_tool: false,
        }),
    }
}

#[cfg(test)]
#[path = "codeview_plan_tests.rs"]
mod tests;
