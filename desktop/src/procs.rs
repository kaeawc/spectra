//! The process feed: `spectra process -json --classify`, every process on the machine with what
//! Spectra could tell about it (its role, runtime, owning app and signature).

use std::borrow::Cow;
use std::process::Command;
use std::time::Duration;

use serde::Deserialize;

use crate::run::{self, RunError};

/// What a process is for, as Spectra classifies it.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash, PartialOrd, Ord, Deserialize)]
#[serde(from = "String")]
pub enum Role {
    App,
    AppHelper,
    AiAgent,
    Terminal,
    Shell,
    Cli,
    System,
    #[default]
    Other,
}

impl From<String> for Role {
    fn from(s: String) -> Role {
        match s.as_str() {
            "app" => Role::App,
            "app_helper" => Role::AppHelper,
            "ai_agent" => Role::AiAgent,
            "terminal" => Role::Terminal,
            "shell" => Role::Shell,
            "cli" => Role::Cli,
            "system" => Role::System,
            _ => Role::Other,
        }
    }
}

impl Role {
    pub fn label(self) -> &'static str {
        match self {
            Role::App => "App",
            Role::AppHelper => "App helper",
            Role::AiAgent => "AI agent",
            Role::Terminal => "Terminal",
            Role::Shell => "Shell",
            Role::Cli => "Tool",
            Role::System => "System",
            Role::Other => "Other",
        }
    }
}

/// What a process is built with or runs on.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash, PartialOrd, Ord, Deserialize)]
#[serde(from = "String")]
pub enum Runtime {
    Electron,
    Jvm,
    Node,
    Bun,
    Deno,
    Go,
    Rust,
    Python,
    Ruby,
    Shell,
    Native,
    #[default]
    Unknown,
}

impl From<String> for Runtime {
    fn from(s: String) -> Runtime {
        match s.as_str() {
            "electron" => Runtime::Electron,
            "jvm" => Runtime::Jvm,
            "node" => Runtime::Node,
            "bun" => Runtime::Bun,
            "deno" => Runtime::Deno,
            "go" => Runtime::Go,
            "rust" => Runtime::Rust,
            "python" => Runtime::Python,
            "ruby" => Runtime::Ruby,
            "shell" => Runtime::Shell,
            "native" => Runtime::Native,
            _ => Runtime::Unknown,
        }
    }
}

impl Runtime {
    /// Every runtime, in the order the sidebar lists them.
    pub const ALL: [Runtime; 12] = [
        Runtime::Electron,
        Runtime::Jvm,
        Runtime::Node,
        Runtime::Bun,
        Runtime::Deno,
        Runtime::Go,
        Runtime::Rust,
        Runtime::Python,
        Runtime::Ruby,
        Runtime::Shell,
        Runtime::Native,
        Runtime::Unknown,
    ];

    pub fn label(self) -> &'static str {
        match self {
            Runtime::Electron => "Electron",
            Runtime::Jvm => "JVM",
            Runtime::Node => "Node",
            Runtime::Bun => "Bun",
            Runtime::Deno => "Deno",
            Runtime::Go => "Go",
            Runtime::Rust => "Rust",
            Runtime::Python => "Python",
            Runtime::Ruby => "Ruby",
            Runtime::Shell => "Shell script",
            Runtime::Native => "Native",
            Runtime::Unknown => "Unknown",
        }
    }

    /// A stable id for keys.
    pub fn id(self) -> &'static str {
        match self {
            Runtime::Electron => "electron",
            Runtime::Jvm => "jvm",
            Runtime::Node => "node",
            Runtime::Bun => "bun",
            Runtime::Deno => "deno",
            Runtime::Go => "go",
            Runtime::Rust => "rust",
            Runtime::Python => "python",
            Runtime::Ruby => "ruby",
            Runtime::Shell => "shell",
            Runtime::Native => "native",
            Runtime::Unknown => "unknown",
        }
    }
}

/// How a process's executable is signed. Ordered worst first, so the worst of a group sorts
/// lowest.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash, PartialOrd, Ord, Deserialize)]
#[serde(from = "String")]
pub enum SignStatus {
    Invalid,
    Unsigned,
    Adhoc,
    #[default]
    Unknown,
    DeveloperId,
    Apple,
}

impl From<String> for SignStatus {
    fn from(s: String) -> SignStatus {
        match s.as_str() {
            "apple" => SignStatus::Apple,
            "developer_id" => SignStatus::DeveloperId,
            "adhoc" => SignStatus::Adhoc,
            "unsigned" => SignStatus::Unsigned,
            "invalid" => SignStatus::Invalid,
            _ => SignStatus::Unknown,
        }
    }
}

impl SignStatus {
    pub fn label(self) -> &'static str {
        match self {
            SignStatus::Apple => "Apple",
            SignStatus::DeveloperId => "Developer ID",
            SignStatus::Adhoc => "Ad hoc",
            SignStatus::Unsigned => "Unsigned",
            SignStatus::Invalid => "Invalid",
            SignStatus::Unknown => "Unknown",
        }
    }

    /// Unsigned or ad hoc: runs, but nobody identified stands behind it.
    pub fn is_unidentified(self) -> bool {
        matches!(self, SignStatus::Unsigned | SignStatus::Adhoc)
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct AppInfo {
    pub name: String,
    pub bundle_path: String,
    pub bundle_id: String,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct Signing {
    pub status: SignStatus,
    pub team_id: String,
    pub hardened_runtime: bool,
    /// Whether Spectra checked the signature against its anchor rather than only reading what
    /// `codesign -dv` displays. Older Spectras leave it out; the evidence says it then.
    pub validated: Option<bool>,
}

/// A signature as the list shows it: what it claims and whether that was checked. Orders least
/// trusted first, so a group's is its worst member's.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum Trust {
    Invalid,
    Unsigned,
    Adhoc,
    #[default]
    Unknown,
    /// Claims Developer ID or Apple, as displayed; nobody checked it.
    Unverified(SignStatus),
    /// Checked against its anchor.
    Verified(SignStatus),
}

impl Trust {
    pub fn label(self) -> String {
        match self {
            Trust::Invalid => "Invalid".into(),
            Trust::Unsigned => "Unsigned".into(),
            Trust::Adhoc => "Ad hoc".into(),
            Trust::Unknown => "Unknown".into(),
            Trust::Unverified(s) => format!("{} · not verified", s.label()),
            Trust::Verified(s) => s.label().into(),
        }
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Process {
    pub pid: u32,
    pub ppid: u32,
    /// macOS's `nobody` is -2.
    pub uid: i64,
    pub user: String,
    pub command: String,
    pub executable_path: String,
    pub full_command_line: String,
    pub rss_kib: f64,
    pub vsize_kib: f64,
    pub thread_count: u64,
    pub cpu_pct: f64,
    pub mem_percent: f64,
    pub started_at: String,
    pub role: Role,
    pub runtime: Runtime,
    pub app: Option<AppInfo>,
    pub signing: Option<Signing>,
    pub evidence: Vec<String>,
    /// Nanoseconds it had been running at the reading.
    pub elapsed: Option<i64>,
}

/// Whether the `signing:` evidence line says the signature was checked. Spectra writes
/// "validated by codesign --verify" for a checked one and "not validated" otherwise.
fn evidence_validated(evidence: &[String]) -> bool {
    evidence
        .iter()
        .find(|l| l.starts_with("signing:"))
        .is_some_and(|l| l.contains(" validated by ") && !l.contains("not validated"))
}

impl Process {
    /// The short name: its command, else its executable's file name.
    pub fn name(&self) -> Cow<'_, str> {
        if !self.command.is_empty() {
            return Cow::Borrowed(&self.command);
        }
        match self.executable_path.rsplit('/').find(|p| !p.is_empty()) {
            Some(n) => Cow::Borrowed(n),
            None => Cow::Owned(format!("pid {}", self.pid)),
        }
    }

    pub fn sign_status(&self) -> SignStatus {
        self.signing.as_ref().map_or(SignStatus::Unknown, |s| s.status)
    }

    /// Whether its signature was checked: Spectra's `validated` when it sends one, else what
    /// the evidence says, else not.
    pub fn validated(&self) -> bool {
        self.signing
            .as_ref()
            .is_some_and(|s| s.validated.unwrap_or_else(|| evidence_validated(&self.evidence)))
    }

    /// Settles what can be worked out once per reading, so the list doesn't redo it per row.
    pub fn settle(&mut self) {
        let validated = evidence_validated(&self.evidence);
        if let Some(s) = &mut self.signing {
            s.validated.get_or_insert(validated);
        }
    }

    pub fn trust(&self) -> Trust {
        match self.sign_status() {
            SignStatus::Invalid => Trust::Invalid,
            SignStatus::Unsigned => Trust::Unsigned,
            SignStatus::Adhoc => Trust::Adhoc,
            SignStatus::Unknown => Trust::Unknown,
            s if self.validated() => Trust::Verified(s),
            s => Trust::Unverified(s),
        }
    }

    /// The `signing:` line of its evidence, if Spectra gave one.
    pub fn signing_evidence(&self) -> Option<&str> {
        self.evidence.iter().find(|l| l.starts_with("signing:")).map(String::as_str)
    }

    /// Seconds it had been running at `now` (Unix seconds), from `started_at`, else from the
    /// reading's `elapsed`. `None` when neither says.
    pub fn age_secs(&self, now: i64) -> Option<i64> {
        parse_rfc3339(&self.started_at)
            .map(|t| now - t)
            .or_else(|| self.elapsed.filter(|e| *e > 0).map(|e| e / 1_000_000_000))
    }

    pub fn bundle_path(&self) -> Option<&str> {
        self.app.as_ref().map(|a| a.bundle_path.as_str()).filter(|p| !p.is_empty())
    }

    /// When it started, as `2026-11-19 19:28` in the time zone Spectra reported.
    pub fn started(&self) -> String {
        fmt_start(&self.started_at)
    }

    pub fn sample(&self) -> ProcSample {
        ProcSample { cpu: self.cpu_pct, rss_kib: self.rss_kib, threads: self.thread_count }
    }
}

/// `2026-11-19T19:28:38-05:00` as `2026-11-19 19:28`, in the time zone it was given in.
pub fn fmt_start(s: &str) -> String {
    match (s.get(..10), s.get(11..16)) {
        (Some(d), Some(t)) if s.as_bytes().get(10) == Some(&b'T') => format!("{d} {t}"),
        _ => s.to_owned(),
    }
}

/// `2026-11-19T19:28:38-05:00` (or `Z`) as Unix seconds.
pub fn parse_rfc3339(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || !matches!(b[10], b'T' | b't' | b' ') || b[13] != b':' || b[16] != b':' {
        return None;
    }
    let num = |r: std::ops::Range<usize>| s.get(r)?.parse::<i64>().ok();
    let (y, mo, d) = (num(0..4)?, num(5..7)?, num(8..10)?);
    let (h, mi, sec) = (num(11..13)?, num(14..16)?, num(17..19)?);
    // Skip fractional seconds.
    let mut i = 19;
    if b.get(i) == Some(&b'.') {
        i += 1;
        while b.get(i).is_some_and(u8::is_ascii_digit) {
            i += 1;
        }
    }
    let offset = match b.get(i)? {
        b'Z' | b'z' => 0,
        sign @ (b'+' | b'-') => {
            let (oh, om) = (num(i + 1..i + 3)?, num(i + 4..i + 6)?);
            let o = oh * 3600 + om * 60;
            if *sign == b'-' { -o } else { o }
        }
        _ => return None,
    };
    if !(1..=12).contains(&mo) || !(1..=31).contains(&d) {
        return None;
    }
    // Days from 1970-01-01 to y-mo-d (Howard Hinnant's days_from_civil).
    let y = if mo <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (mo + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    Some(days * 86_400 + h * 3600 + mi * 60 + sec - offset)
}

/// One reading of a process, kept in its history.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct ProcSample {
    pub cpu: f64,
    pub rss_kib: f64,
    pub threads: u64,
}

#[derive(Clone, Debug, PartialEq)]
pub enum ProcError {
    /// The Spectra it ran doesn't know `--classify`.
    TooOld(String),
    Failed(String),
}

pub type ProcFeed = Result<Vec<Process>, ProcError>;

/// Reads the feed. Entries that don't parse are skipped rather than failing the whole list.
pub fn parse(json: &str) -> ProcFeed {
    let raw: Vec<serde_json::Value> =
        serde_json::from_str(json).map_err(|e| ProcError::Failed(format!("Couldn't read Spectra's output: {e}")))?;
    // Without --classify every entry lacks a role; a Spectra that ignored the flag is too old.
    if !raw.is_empty() && raw.iter().all(|v| v.get("role").is_none()) {
        return Err(ProcError::TooOld("Spectra listed processes without classifying them.".into()));
    }
    let mut procs: Vec<Process> = raw.into_iter().filter_map(|v| serde_json::from_value(v).ok()).collect();
    // Every machine runs something; an empty list means the reading went wrong.
    if procs.is_empty() {
        return Err(ProcError::Failed("Spectra listed no processes.".into()));
    }
    procs.sort_by_key(|p| p.pid);
    for p in &mut procs {
        p.settle();
    }
    Ok(procs)
}

/// What a failed run means: Go's flag package names an unknown flag this way.
pub fn classify_failure(bin: &str, stderr: &str) -> ProcError {
    let stderr = stderr.trim();
    if stderr.contains("not defined: -classify") || stderr.contains("unknown command") {
        ProcError::TooOld(format!("{bin} can't classify processes (it has no --classify)."))
    } else {
        let first = stderr.lines().next().unwrap_or("no output");
        ProcError::Failed(format!("{bin} process failed: {first}"))
    }
}

/// How long a reading may take before it's stopped. A cold classification takes about 20 s.
pub const DEADLINE: Duration = Duration::from_secs(30);
/// The most a reading may print; a busy machine's is a few MB.
pub const MAX_OUTPUT: usize = 64 << 20;

/// What a run that didn't finish means, for `bin`.
pub fn run_failure(bin: &str, e: RunError) -> String {
    match e {
        RunError::Spawn(e) => format!("Couldn't run {bin}: {e}. Install Spectra or set SPECTRA_BIN."),
        RunError::Timeout(d) => format!("{bin} took longer than {} s and was stopped", d.as_secs_f64().round()),
        RunError::TooLarge(cap) => format!("{bin} printed more than {} MB and was stopped", cap >> 20),
        RunError::Io(e) => format!("Couldn't read {bin}'s output: {e}"),
    }
}

/// Runs `spectra process -json --classify`. Call it off the UI thread.
pub fn fetch() -> ProcFeed {
    let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
    fetch_from(&bin, DEADLINE)
}

/// [`fetch`] from `bin`, stopping it after `deadline`.
pub fn fetch_from(bin: &str, deadline: Duration) -> ProcFeed {
    let mut cmd = Command::new(bin);
    cmd.args(["process", "-json", "--classify"]);
    let out = run::run(cmd, deadline, MAX_OUTPUT).map_err(|e| ProcError::Failed(run_failure(bin, e)))?;
    if !out.status.success() {
        return Err(classify_failure(bin, &String::from_utf8_lossy(&out.stderr)));
    }
    let stdout = String::from_utf8_lossy(&out.stdout);
    // Go's flag package prints usage to stderr but can exit 0; an empty stdout then is the tell.
    if stdout.trim().is_empty() {
        return Err(classify_failure(bin, &String::from_utf8_lossy(&out.stderr)));
    }
    parse(&stdout)
}

/// Memory, from KiB: one decimal under 10 MB, whole MB below a GB, then GB. The thresholds are
/// where the rounded figure would reach the next unit, so 1023.96 MB reads 1.0 GB, not 1024 MB.
pub fn fmt_mem(kib: f64) -> String {
    let mb = kib / 1024.0;
    if mb >= 1023.5 {
        format!("{:.1} GB", mb / 1024.0)
    } else if mb >= 9.95 {
        format!("{mb:.0} MB")
    } else {
        format!("{mb:.1} MB")
    }
}

pub fn fmt_cpu(pct: f64) -> String {
    format!("{pct:.1}%")
}

/// `1971` as `1,971`.
pub fn fmt_count(n: u64) -> String {
    let digits = n.to_string();
    let mut out = String::new();
    for (i, ch) in digits.chars().enumerate() {
        if i > 0 && (digits.len() - i).is_multiple_of(3) {
            out.push(',');
        }
        out.push(ch);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// One entry as the real feed prints it, extra fields and all.
    const REAL: &str = r#"[{"pid":1836,"ppid":97562,"uid":501,"user":"user","command":"qemu-system-aarch64-headless",
        "bsd_name":"qemu-system-aarch64-headless","executable_path":"/Users/x/qemu-system-aarch64-headless",
        "full_command_line":"/Users/x/qemu-system-aarch64-headless -avd fold -no-window","rss_kib":8622592,
        "vsize_kib":441676384,"vsz_bytes":452276617216,"thread_count":162,"cpu_pct":211.6,"cpu_percent":211.6,
        "mem_percent":6.42,"start_time":"2026-11-19T19:28:38-05:00","started_at":"2026-11-19T19:28:38-05:00",
        "elapsed":201000000000,"role":"cli","runtime":"native",
        "signing":{"status":"developer_id","team_id":"EQHXZ8M8AV","hardened_runtime":true},
        "evidence":["role: no app, system, shell, terminal or agent signal"]},
        {"pid":73802,"ppid":1,"uid":-2,"user":"nobody","command":"Claude","role":"app","runtime":"electron",
        "app":{"name":"Claude","bundle_path":"/Applications/Claude.app","bundle_id":"com.anthropic.claudefordesktop"},
        "signing":{"status":"developer_id"}}]"#;

    #[test]
    fn parses_the_real_feeds_shape() {
        let p = parse(REAL).unwrap();
        assert_eq!(p.len(), 2);
        let q = &p[0];
        assert_eq!((q.pid, q.role, q.runtime), (1836, Role::Cli, Runtime::Native));
        assert_eq!(q.sign_status(), SignStatus::DeveloperId);
        assert!(q.signing.as_ref().unwrap().hardened_runtime);
        assert_eq!(q.rss_kib, 8_622_592.0);
        assert_eq!(q.started(), "2026-11-19 19:28");
        let c = &p[1];
        assert_eq!(c.uid, -2);
        assert_eq!(c.bundle_path(), Some("/Applications/Claude.app"));
        assert_eq!(c.runtime, Runtime::Electron);
    }

    #[test]
    fn missing_fields_take_defaults_and_unknown_words_dont_fail() {
        let p = parse(r#"[{"pid":5,"role":"martian","runtime":"cobol","signing":{"status":"weird"}},{"pid":4,"role":"shell"}]"#)
            .unwrap();
        assert_eq!(p[0].pid, 4, "sorted by pid");
        assert_eq!(p[1].role, Role::Other);
        assert_eq!(p[1].runtime, Runtime::Unknown);
        assert_eq!(p[1].sign_status(), SignStatus::Unknown);
        assert_eq!(p[0].sign_status(), SignStatus::Unknown, "no signing at all");
        assert_eq!(p[0].name(), "pid 4");
        assert_eq!(p[0].started(), "");
    }

    #[test]
    fn a_broken_entry_is_skipped() {
        let p = parse(r#"[{"pid":"seven","role":"cli"},{"pid":8,"role":"cli"}]"#).unwrap();
        assert_eq!(p.iter().map(|p| p.pid).collect::<Vec<_>>(), [8]);
    }

    #[test]
    fn spots_a_spectra_too_old_to_classify() {
        let err = classify_failure("spectra", "flag provided but not defined: -classify\nUsage of spectra process:");
        assert!(matches!(err, ProcError::TooOld(_)));
        assert!(matches!(classify_failure("spectra", "permission denied"), ProcError::Failed(_)));
        assert!(matches!(parse(r#"[{"pid":1,"command":"launchd"}]"#), Err(ProcError::TooOld(_))));
    }

    #[test]
    fn an_empty_list_is_a_failed_reading() {
        assert!(matches!(parse("[]"), Err(ProcError::Failed(_))));
        assert!(matches!(parse(r#"[{"pid":"x","role":"cli"}]"#), Err(ProcError::Failed(_))), "nothing parsed");
    }

    /// The real feed's `signing:` lines, team ids changed.
    const DEV_ID: &str = "signing: developer_id, team ABCDE12345 as displayed by codesign -dv, not validated; verdict is for the file on disk";
    const APPLE: &str = r#"signing: platform binary validated by codesign --verify --ignore-resources -R="anchor apple"; verdict is for the file on disk"#;

    #[test]
    fn a_signature_is_verified_only_when_spectra_checked_it() {
        let feed = format!(
            r#"[{{"pid":1,"role":"system","signing":{{"status":"apple"}},"evidence":[{APPLE:?}]}},
               {{"pid":2,"role":"app","signing":{{"status":"developer_id","team_id":"ABCDE12345"}},"evidence":[{DEV_ID:?}]}},
               {{"pid":3,"role":"cli","signing":{{"status":"developer_id"}}}},
               {{"pid":4,"role":"cli","signing":{{"status":"developer_id","validated":true}},"evidence":[{DEV_ID:?}]}},
               {{"pid":5,"role":"cli","signing":{{"status":"apple","validated":false}},"evidence":[{APPLE:?}]}}]"#
        );
        let p = parse(&feed).unwrap();
        let trust: Vec<Trust> = p.iter().map(Process::trust).collect();
        assert_eq!(
            trust,
            [
                Trust::Verified(SignStatus::Apple),
                Trust::Unverified(SignStatus::DeveloperId),
                Trust::Unverified(SignStatus::DeveloperId),
                Trust::Verified(SignStatus::DeveloperId),
                Trust::Unverified(SignStatus::Apple),
            ],
            "evidence decides unless Spectra says; no word is not verified"
        );
        assert_eq!(trust[1].label(), "Developer ID · not verified");
        assert_eq!(trust[0].label(), "Apple");
        assert_eq!(p[1].signing_evidence(), Some(DEV_ID));
        // Least trusted first: an unverified Developer ID sits below every verified one.
        let mut all = trust.clone();
        all.extend([Trust::Adhoc, Trust::Invalid, Trust::Unknown]);
        all.sort();
        assert_eq!(&all[..4], [Trust::Invalid, Trust::Adhoc, Trust::Unknown, Trust::Unverified(SignStatus::DeveloperId)]);
        assert_eq!(all.last(), Some(&Trust::Verified(SignStatus::Apple)));
    }

    #[test]
    fn reads_start_times_and_ages() {
        assert_eq!(parse_rfc3339("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_rfc3339("2026-11-19T19:28:38-05:00"), Some(1_795_134_518));
        assert_eq!(parse_rfc3339("2026-11-20T00:28:38.123+00:00"), Some(1_795_134_518));
        assert_eq!(parse_rfc3339("yesterday"), None);
        let mut p = Process { started_at: "2026-11-19T19:28:38-05:00".into(), ..Process::default() };
        assert_eq!(p.age_secs(1_795_134_518 + 90), Some(90));
        p.started_at.clear();
        p.elapsed = Some(201_000_000_000);
        assert_eq!(p.age_secs(0), Some(201), "the reading's elapsed when there's no start time");
        p.elapsed = None;
        assert_eq!(p.age_secs(0), None);
    }

    #[test]
    fn a_hung_spectra_is_stopped_and_reported() {
        let bin = crate::run::tests::script("hung-process", "exec sleep 60");
        let err = fetch_from(bin.to_str().unwrap(), Duration::from_millis(200)).unwrap_err();
        let ProcError::Failed(why) = err else { panic!("{err:?}") };
        assert!(why.contains("took longer than") && why.contains("was stopped"), "{why}");
    }

    #[test]
    fn a_spectra_that_lists_nothing_has_failed() {
        let bin = crate::run::tests::script("empty-process", "echo '[]'");
        assert!(matches!(fetch_from(bin.to_str().unwrap(), DEADLINE), Err(ProcError::Failed(_))));
    }

    #[test]
    fn formats_memory_cpu_and_counts() {
        assert_eq!(fmt_mem(736.0), "0.7 MB");
        assert_eq!(fmt_mem(272_464.0), "266 MB");
        assert_eq!(fmt_mem(8_622_592.0), "8.2 GB");
        assert_eq!(fmt_mem(1023.96 * 1024.0), "1.0 GB", "rounds up into GB, never 1024 MB");
        assert_eq!(fmt_mem(1023.4 * 1024.0), "1023 MB");
        assert_eq!(fmt_mem(9.97 * 1024.0), "10 MB");
        assert_eq!(fmt_cpu(211.64), "211.6%");
        assert_eq!(fmt_cpu(0.0), "0.0%");
        assert_eq!(fmt_count(1971), "1,971");
        assert_eq!(fmt_count(12), "12");
    }

    #[test]
    fn worst_signing_sorts_first() {
        let mut s = [SignStatus::Apple, SignStatus::Adhoc, SignStatus::Invalid, SignStatus::DeveloperId];
        s.sort();
        assert_eq!(s[0], SignStatus::Invalid);
        assert_eq!(s[3], SignStatus::Apple);
    }

    /// Needs a Spectra with `process --classify`: `SPECTRA_BIN=… cargo test -- --ignored`.
    #[test]
    #[ignore]
    fn fetches_the_live_process_feed() {
        let procs = fetch().unwrap();
        let count = |f: &dyn Fn(&Process) -> bool| procs.iter().filter(|p| f(p)).count();
        println!("{} processes", procs.len());
        for r in [Role::App, Role::AppHelper, Role::AiAgent, Role::Terminal, Role::Shell, Role::Cli, Role::System, Role::Other] {
            println!("  role {:<11} {}", r.label(), count(&|p| p.role == r));
        }
        for rt in Runtime::ALL {
            println!("  runtime {:<12} {}", rt.label(), count(&|p| p.runtime == rt));
        }
        for st in [SignStatus::Apple, SignStatus::DeveloperId, SignStatus::Adhoc, SignStatus::Unsigned, SignStatus::Invalid, SignStatus::Unknown] {
            println!("  signing {:<12} {}", st.label(), count(&|p| p.sign_status() == st));
        }
        let validated = procs.iter().filter(|p| p.validated()).count();
        println!("  {validated} signatures verified, {} not", procs.iter().filter(|p| p.signing.is_some()).count() - validated);
        let groups = crate::groups::group(&procs.iter().collect::<Vec<_>>(), crate::groups::Sort::default());
        println!("{} groups, {} of them apps", groups.len(), groups.iter().filter(|g| g.kind == crate::groups::GroupKind::App).count());
        for g in groups.iter().filter(|g| g.hint.is_some()).take(8) {
            println!("  same name, told apart: {} [{}] ({} processes)", g.name, g.hint.as_deref().unwrap_or(""), g.pids.len());
        }
        assert!(!procs.is_empty());
    }
}
