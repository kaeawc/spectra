//! Quit and Force quit: which processes may be signalled, the check that a pid still belongs to
//! the process on screen, and sending the signal. Everything goes through [`Signals`], so tests
//! record what would have been sent instead of sending it.

use std::process::Command;
use std::time::Duration;

use crate::procs::{Process, Role, parse_rfc3339};
use crate::run::{self, RunError};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Signal {
    /// SIGTERM: asks it to exit.
    Term,
    /// SIGKILL: ends it at once.
    Kill,
}

impl Signal {
    /// What the button and the confirmation call it.
    pub fn verb(self) -> &'static str {
        match self {
            Signal::Term => "Quit",
            Signal::Kill => "Force quit",
        }
    }

    fn flag(self) -> &'static str {
        match self {
            Signal::Term => "-TERM",
            Signal::Kill => "-KILL",
        }
    }
}

/// What `ps` says a pid is now: when it started (Unix seconds) and its command.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Identity {
    pub started: i64,
    pub comm: String,
}

/// How the app asks `ps` about a pid and signals it. The real one runs `/bin/ps` and
/// `/bin/kill`; tests inject a recorder.
pub trait Signals {
    /// `Ok(None)` when no process has `pid`.
    fn identify(&self, pid: u32) -> Result<Option<Identity>, String>;
    fn send(&self, pid: u32, signal: Signal) -> Result<(), String>;
}

/// Who the app is, to keep it from offering to stop itself, the readings it started, or what
/// another user owns.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Me {
    pub pid: u32,
    /// `None` until known; then other users' processes are refused up front.
    pub uid: Option<i64>,
}

/// The process an action is for, as it was on screen: enough to tell it from whatever has its
/// pid by the time the signal goes, and to name it once it's gone.
#[derive(Clone, Debug)]
pub struct Target {
    pub pid: u32,
    /// The feed's `started_at`.
    pub started: String,
    /// What it's called on screen: an app by its app's name.
    pub name: String,
    pub system: bool,
    /// The process as last read, for a page that outlives it.
    pub process: Process,
}

/// The same process, named the same; the rest of the snapshot doesn't matter.
impl PartialEq for Target {
    fn eq(&self, o: &Target) -> bool {
        (self.pid, &self.started, &self.name, self.system) == (o.pid, &o.started, &o.name, o.system)
    }
}

impl Target {
    pub fn of(p: &Process) -> Target {
        Target {
            pid: p.pid,
            started: p.started_at.clone(),
            name: display_name(p),
            system: p.role == Role::System,
            process: p.clone(),
        }
    }

    /// Whether `p` is this target: the same pid and start.
    pub fn is(&self, p: &Process) -> bool {
        p.pid == self.pid && p.started_at == self.started
    }
}

/// An app by its app's name ("Slack"), anything else by its own.
pub fn display_name(p: &Process) -> String {
    match &p.app {
        Some(app) if p.role == Role::App && !app.name.is_empty() => app.name.clone(),
        _ => p.name().into_owned(),
    }
}

/// Why `p` can't be quit from here, or `None` when it can.
pub fn refusal(p: &Process, me: Me) -> Option<String> {
    if p.pid == 0 {
        return Some("This is the kernel; it can't be stopped.".into());
    }
    if p.pid == 1 {
        return Some(format!("{} starts and looks after everything else; Spectra Desktop won't stop it.", p.name()));
    }
    if p.pid == me.pid {
        return Some("This is Spectra Desktop itself.".into());
    }
    if p.ppid == me.pid {
        return Some("Spectra Desktop started it to take a reading; it ends on its own.".into());
    }
    if let Some(uid) = me.uid
        && uid != 0
        && p.uid != uid
    {
        let owner = if p.user.is_empty() { format!("uid {}", p.uid) } else { p.user.clone() };
        return Some(format!("Owned by {owner}; Spectra Desktop can't stop it"));
    }
    if parse_rfc3339(&p.started_at).is_none() {
        return Some("Spectra didn't say when it started, so Spectra Desktop can't make sure the pid is still the same process.".into());
    }
    None
}

/// What the confirmation says will happen.
pub fn consequence(t: &Target, signal: Signal) -> Vec<String> {
    let mut lines = vec![match signal {
        Signal::Term => format!("{} (pid {}) will be asked to close. Unsaved changes may be lost.", t.name, t.pid),
        Signal::Kill => format!("{} (pid {}) will be stopped immediately. Unsaved changes will be lost.", t.name, t.pid),
    }];
    if t.system {
        lines.push("This is part of macOS and may restart or disrupt your session.".into());
    }
    lines
}

/// How a send went.
#[derive(Clone, Debug, PartialEq)]
pub enum Attempt {
    Sent,
    /// Nothing was sent: the pid is gone, or is now another process.
    Refused(String),
    /// `ps` or `kill` failed; whether anything was sent is in the words.
    Failed(String),
}

/// `ps -o lstart=` with `TZ=UTC0` and `LC_ALL=C` (`Fri Nov  7 08:53:10 2026`) as Unix seconds.
pub fn parse_lstart(s: &str) -> Option<i64> {
    let mut w = s.split_whitespace();
    let (_weekday, month, day, time, year) = (w.next()?, w.next()?, w.next()?, w.next()?, w.next()?);
    if w.next().is_some() {
        return None;
    }
    const MONTHS: [&str; 12] = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    let month = MONTHS.iter().position(|m| m.eq_ignore_ascii_case(month))? + 1;
    let day: u32 = day.parse().ok()?;
    let year: u32 = year.parse().ok()?;
    let t: Vec<&str> = time.split(':').collect();
    if t.len() != 3 || t.iter().any(|p| p.len() != 2 || !p.bytes().all(|b| b.is_ascii_digit())) || !(1..=31).contains(&day) {
        return None;
    }
    parse_rfc3339(&format!("{year:04}-{month:02}-{day:02}T{}:{}:{}Z", t[0], t[1], t[2]))
}

/// One line of `ps -o lstart= -o comm=`: five words of start time, then the command, which may
/// hold spaces.
pub fn parse_ps(line: &str) -> Option<Identity> {
    let line = line.trim();
    let mut rest = line;
    let mut words = 0;
    while words < 5 {
        rest = rest.trim_start();
        let end = rest.find(char::is_whitespace).unwrap_or(rest.len());
        if end == 0 {
            return None;
        }
        rest = &rest[end..];
        words += 1;
    }
    let started = parse_lstart(&line[..line.len() - rest.len()])?;
    Some(Identity { started, comm: rest.trim().to_owned() })
}

fn base(path: &str) -> &str {
    path.rsplit('/').find(|p| !p.is_empty()).unwrap_or(path)
}

/// Whether `ps`'s command is the program the feed listed: its executable's path, or the name
/// at the end of either (a login shell's leading `-` aside). Linux cuts `comm` to 15 bytes.
pub fn same_program(comm: &str, t: &Process) -> bool {
    let comm = comm.trim();
    let theirs = base(comm.trim_start_matches('-'));
    let exe = t.executable_path.as_str();
    let command = t.command.trim_start_matches('-');
    if exe.is_empty() && command.is_empty() {
        return true;
    }
    if !exe.is_empty() && comm == exe {
        return true;
    }
    let names = [(!exe.is_empty()).then(|| base(exe)), (!command.is_empty()).then_some(command)];
    names.into_iter().flatten().any(|n| n == theirs || (theirs.len() == 15 && n.starts_with(theirs)))
}

/// Whether `now` (what `ps` says has the pid) is still `t`, or why not.
pub fn verify(t: &Target, now: Option<&Identity>) -> Result<(), String> {
    let Some(now) = now else {
        return Err("That process has already ended. Nothing was sent.".into());
    };
    let Some(started) = parse_rfc3339(&t.started) else {
        return Err(format!("Spectra didn't say when {} started, so nothing was sent.", t.name));
    };
    if now.started != started {
        return Err("That process has already ended; its pid now belongs to another process. Nothing was sent.".into());
    }
    if !same_program(&now.comm, &t.process) {
        return Err(format!("pid {} now runs {}, not {}. Nothing was sent.", t.pid, now.comm, t.name));
    }
    Ok(())
}

/// Checks that `t`'s pid is still `t`, then sends `signal`, and nothing if the check fails.
pub fn quit(signals: &dyn Signals, t: &Target, signal: Signal) -> Attempt {
    let now = match signals.identify(t.pid) {
        Ok(now) => now,
        Err(e) => return Attempt::Failed(format!("Couldn't check that pid {} is still {}: {e}. Nothing was sent.", t.pid, t.name)),
    };
    if let Err(why) = verify(t, now.as_ref()) {
        return Attempt::Refused(why);
    }
    match signals.send(t.pid, signal) {
        Ok(()) => Attempt::Sent,
        Err(e) => Attempt::Failed(format!("Couldn't {} {}: {e}.", signal.verb().to_lowercase(), t.name)),
    }
}

/// How long `ps` or `kill` may take.
const DEADLINE: Duration = Duration::from_secs(5);
const MAX_OUTPUT: usize = 64 << 10;

/// Signals nothing: sample scenes use it, so a scene can't stop a real process with a pid
/// from the sample.
pub struct Inert;

impl Signals for Inert {
    fn identify(&self, _: u32) -> Result<Option<Identity>, String> {
        Err("this is sample data".into())
    }

    fn send(&self, _: u32, _: Signal) -> Result<(), String> {
        Err("this is sample data".into())
    }
}

/// The user the app runs as, from `id -u`.
pub fn my_uid() -> Option<i64> {
    let mut cmd = Command::new("/usr/bin/id");
    cmd.arg("-u");
    let out = run::run(cmd, DEADLINE, MAX_OUTPUT).ok().filter(|o| o.status.success())?;
    String::from_utf8_lossy(&out.stdout).trim().parse().ok()
}

/// The real thing: `/bin/ps` and `/bin/kill`, each with a deadline.
pub struct System;

fn failure(what: &str, e: RunError) -> String {
    match e {
        RunError::Spawn(e) => format!("couldn't run {what}: {e}"),
        RunError::Timeout(d) => format!("{what} took longer than {} s", d.as_secs()),
        RunError::TooLarge(_) => format!("{what} printed too much"),
        RunError::Io(e) => format!("couldn't read {what}'s output: {e}"),
    }
}

/// `kill` reads its pid into a signed `pid_t`, where 0 means "my process group" and anything that
/// wraps negative means "every process I may signal", so only 2..=i32::MAX may reach it.
fn signalable(pid: u32) -> bool {
    pid > 1 && i32::try_from(pid).is_ok()
}

impl Signals for System {
    fn identify(&self, pid: u32) -> Result<Option<Identity>, String> {
        let mut cmd = Command::new("/bin/ps");
        // UTC and English, so the start time reads the same on every machine.
        cmd.env("TZ", "UTC0").env("LC_ALL", "C").args(["-o", "lstart=", "-o", "comm=", "-p", &pid.to_string()]);
        let out = run::run(cmd, DEADLINE, MAX_OUTPUT).map_err(|e| failure("ps", e))?;
        let stdout = String::from_utf8_lossy(&out.stdout);
        let Some(line) = stdout.lines().find(|l| !l.trim().is_empty()) else {
            // `ps -p` prints nothing and exits 1 for a pid nobody has.
            return if out.status.code() == Some(1) && out.stderr.iter().all(u8::is_ascii_whitespace) {
                Ok(None)
            } else {
                Err(format!("ps failed: {}", String::from_utf8_lossy(&out.stderr).trim()))
            };
        };
        parse_ps(line).map(Some).ok_or_else(|| format!("couldn't read ps's answer: {}", line.trim()))
    }

    fn send(&self, pid: u32, signal: Signal) -> Result<(), String> {
        if !signalable(pid) {
            return Err(format!("refused to signal pid {pid}"));
        }
        let mut cmd = Command::new("/bin/kill");
        cmd.args([signal.flag(), &pid.to_string()]);
        let out = run::run(cmd, DEADLINE, MAX_OUTPUT).map_err(|e| failure("kill", e))?;
        if out.status.success() {
            return Ok(());
        }
        let why = String::from_utf8_lossy(&out.stderr);
        // `kill: 900: Operation not permitted` → the reason alone.
        let why = why.trim().rsplit(": ").next().filter(|w| !w.is_empty()).unwrap_or("no reason given");
        Err(format!("kill failed: {why}"))
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::cell::RefCell;

    #[test]
    fn only_ordinary_pids_can_reach_kill() {
        for pid in [0, 1, 2_147_483_648, u32::MAX] {
            assert!(!signalable(pid), "{pid}");
            assert!(System.send(pid, Signal::Term).is_err(), "{pid}");
        }
        assert!(signalable(2) && signalable(i32::MAX as u32));
    }
    use std::rc::Rc;

    /// Answers `identify` from a table and records every signal instead of sending it.
    #[derive(Clone, Default)]
    pub struct Recorder {
        pub alive: Rc<RefCell<Vec<(u32, Identity)>>>,
        pub sent: Rc<RefCell<Vec<(u32, Signal)>>>,
        pub fail_kill: Rc<RefCell<Option<String>>>,
        pub fail_ps: Rc<RefCell<Option<String>>>,
        pub asked: Rc<RefCell<Vec<u32>>>,
    }

    impl Recorder {
        pub fn alive(&self, pid: u32, started_at: &str, comm: &str) {
            let started = parse_rfc3339(started_at).expect("an RFC 3339 time");
            self.alive.borrow_mut().push((pid, Identity { started, comm: comm.into() }));
        }

        pub fn sent(&self) -> Vec<(u32, Signal)> {
            self.sent.borrow().clone()
        }
    }

    impl Signals for Recorder {
        fn identify(&self, pid: u32) -> Result<Option<Identity>, String> {
            self.asked.borrow_mut().push(pid);
            if let Some(e) = self.fail_ps.borrow().clone() {
                return Err(e);
            }
            Ok(self.alive.borrow().iter().find(|(p, _)| *p == pid).map(|(_, id)| id.clone()))
        }

        fn send(&self, pid: u32, signal: Signal) -> Result<(), String> {
            if let Some(e) = self.fail_kill.borrow().clone() {
                return Err(e);
            }
            self.sent.borrow_mut().push((pid, signal));
            Ok(())
        }
    }

    fn slack() -> Process {
        Process {
            pid: 900,
            ppid: 1,
            uid: 501,
            user: "jason".into(),
            command: "Slack".into(),
            executable_path: "/Applications/Slack.app/Contents/MacOS/Slack".into(),
            started_at: "2026-11-19T19:28:38-05:00".into(),
            role: Role::App,
            ..Process::default()
        }
    }

    const ME: Me = Me { pid: 5000, uid: Some(501) };

    #[test]
    fn reads_lstart_in_utc_with_single_digit_days() {
        assert_eq!(parse_lstart("Fri Nov 20 00:28:38 2026"), parse_rfc3339("2026-11-19T19:28:38-05:00"));
        // A one-digit day is padded with a space.
        assert_eq!(parse_lstart("Fri Nov  7 08:05:01 2026"), parse_rfc3339("2026-11-07T08:05:01Z"));
        assert_eq!(parse_lstart("Sat Feb 29 23:59:59 2028"), parse_rfc3339("2028-03-01T05:29:59+05:30"));
        assert_eq!(parse_lstart("Thu Jan  1 00:00:00 1970"), Some(0));
        for bad in ["", "Fri Nov 20 2026", "Fri Nov 20 0:28:38 2026", "Fri Nvb 20 00:28:38 2026", "Fri Nov 32 00:28:38 2026", "Fri Nov 20 00:28:38 2026 x"] {
            assert_eq!(parse_lstart(bad), None, "{bad:?}");
        }
    }

    #[test]
    fn a_ps_line_keeps_a_command_with_spaces() {
        let id = parse_ps("Fri Nov  7 08:05:01 2026     /Applications/Slack.app/Contents/Frameworks/Slack Helper (GPU).app/Contents/MacOS/Slack Helper (GPU)\n").unwrap();
        assert_eq!(id.started, parse_rfc3339("2026-11-07T08:05:01Z").unwrap());
        assert_eq!(id.comm, "/Applications/Slack.app/Contents/Frameworks/Slack Helper (GPU).app/Contents/MacOS/Slack Helper (GPU)");
        assert_eq!(parse_ps("Fri Nov 7 08:05:01 2026 sleep").unwrap().comm, "sleep");
        assert_eq!(parse_ps("Fri Nov 7 08:05:01"), None);
    }

    #[test]
    fn the_same_second_in_another_zone_matches_and_the_next_second_does_not() {
        let t = Target::of(&slack());
        let exe = "/Applications/Slack.app/Contents/MacOS/Slack";
        let at = |lstart: &str| Identity { started: parse_lstart(lstart).unwrap(), comm: exe.into() };
        assert_eq!(verify(&t, Some(&at("Fri Nov 20 00:28:38 2026"))), Ok(()), "-05:00 is 00:28:38 UTC");
        let mut ist = t.clone();
        ist.started = "2026-11-20T05:58:38.900+05:30".into();
        assert_eq!(verify(&ist, Some(&at("Fri Nov 20 00:28:38 2026"))), Ok(()), "fractions are a format, not a second");
        assert!(verify(&t, Some(&at("Fri Nov 20 00:28:39 2026"))).unwrap_err().contains("already ended"));
        assert!(verify(&t, Some(&at("Thu Nov 19 19:28:38 2026"))).is_err(), "local time read as UTC is a different instant");
        assert_eq!(verify(&t, None).unwrap_err(), "That process has already ended. Nothing was sent.");
    }

    #[test]
    fn the_program_must_match_too() {
        let p = slack();
        assert!(same_program("/Applications/Slack.app/Contents/MacOS/Slack", &p));
        assert!(same_program("Slack", &p), "a bare name");
        assert!(!same_program("/usr/bin/node", &p));
        let mut zsh = Process { command: "zsh".into(), executable_path: "/bin/zsh".into(), ..Process::default() };
        assert!(same_program("-zsh", &zsh), "a login shell");
        zsh.command = "deploy.sh".into();
        zsh.executable_path = "/bin/bash".into();
        assert!(same_program("/bin/bash", &zsh));
        let long = Process { command: "qemu-system-aarch64-headless".into(), ..Process::default() };
        assert!(same_program("qemu-system-aar", &long), "Linux's 15-byte comm");
        assert!(!same_program("qemu-system-x86", &long));
        let t = Target::of(&slack());
        let id = Identity { started: parse_rfc3339(&t.started).unwrap(), comm: "/usr/bin/node".into() };
        assert_eq!(verify(&t, Some(&id)).unwrap_err(), "pid 900 now runs /usr/bin/node, not Slack. Nothing was sent.");
    }

    #[test]
    fn quit_sends_one_signal_only_after_the_check() {
        let r = Recorder::default();
        let t = Target::of(&slack());
        assert!(matches!(quit(&r, &t, Signal::Term), Attempt::Refused(_)), "gone");
        assert!(r.sent().is_empty());
        r.alive(900, "2026-11-20T00:28:38Z", "/Applications/Slack.app/Contents/MacOS/Slack");
        assert_eq!(quit(&r, &t, Signal::Kill), Attempt::Sent);
        assert_eq!(r.sent(), [(900, Signal::Kill)]);
        *r.fail_ps.borrow_mut() = Some("ps took longer than 5 s".into());
        let Attempt::Failed(why) = quit(&r, &t, Signal::Term) else { panic!() };
        assert!(why.contains("Nothing was sent"), "{why}");
        assert_eq!(r.sent().len(), 1);
    }

    #[test]
    fn refuses_the_kernel_launchd_itself_its_readings_other_users_and_unknown_starts() {
        let ok = slack();
        assert_eq!(refusal(&ok, ME), None);
        let with = |f: &dyn Fn(&mut Process)| {
            let mut p = slack();
            f(&mut p);
            refusal(&p, ME)
        };
        assert!(with(&|p| p.pid = 0).unwrap().contains("kernel"));
        assert!(with(&|p| p.pid = 1).unwrap().contains("won't stop it"));
        assert_eq!(with(&|p| p.pid = ME.pid).as_deref(), Some("This is Spectra Desktop itself."));
        assert!(with(&|p| p.ppid = ME.pid).unwrap().contains("to take a reading"));
        assert_eq!(with(&|p| { p.uid = 0; p.user = "root".into() }).as_deref(), Some("Owned by root; Spectra Desktop can't stop it"));
        assert_eq!(with(&|p| { p.uid = -2; p.user.clear() }).as_deref(), Some("Owned by uid -2; Spectra Desktop can't stop it"));
        assert!(with(&|p| p.started_at.clear()).unwrap().contains("didn't say when it started"));
        assert!(with(&|p| p.started_at = "yesterday".into()).is_some());
        // Its parent is not refused.
        assert_eq!(with(&|p| p.pid = 4999), None);
        // Run as root, it may stop anyone's; unsure who it is, the kill says.
        let mut root = slack();
        root.uid = 0;
        assert_eq!(refusal(&root, Me { pid: 5000, uid: Some(0) }), None);
        assert_eq!(refusal(&root, Me { pid: 5000, uid: None }), None);
    }

    #[test]
    fn the_confirmation_says_what_will_happen_in_plain_words() {
        let t = Target::of(&slack());
        assert_eq!(consequence(&t, Signal::Term), ["Slack (pid 900) will be asked to close. Unsaved changes may be lost."]);
        assert_eq!(consequence(&t, Signal::Kill), ["Slack (pid 900) will be stopped immediately. Unsaved changes will be lost."]);
        let mut sys = slack();
        sys.role = Role::System;
        sys.command = "trustd".into();
        let lines = consequence(&Target::of(&sys), Signal::Term);
        assert_eq!(lines[1], "This is part of macOS and may restart or disrupt your session.");
    }

    /// Spawns `sleep 600` and makes sure it's gone however the test ends.
    struct Child(std::process::Child);

    impl Drop for Child {
        fn drop(&mut self) {
            // Our own child, by the handle that spawned it.
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    fn unix_now() -> i64 {
        std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64
    }

    /// The app's own path (a reading, Quit, the confirmation) with the real `ps` and `kill`, on
    /// a `sleep` this test started and still holds, so its pid can't pass to another process:
    /// a start time a second off sends nothing, the right one quits it.
    #[test]
    fn quits_its_own_sleep_child_through_ps_and_kill() {
        // Spawn within one wall-clock second, so the test knows the start time without `ps`.
        let (mut child, started) = loop {
            let before = unix_now();
            let c = Child(Command::new("/bin/sleep").arg("600").spawn().expect("sleep runs"));
            if unix_now() == before {
                break (c, before);
            }
        };
        let pid = child.0.id();
        let at = |secs: i64| {
            let (d, s) = (secs.div_euclid(86_400), secs.rem_euclid(86_400));
            // Days back to a civil date (Howard Hinnant's civil_from_days).
            let z = d + 719_468;
            let era = z.div_euclid(146_097);
            let doe = z - era * 146_097;
            let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
            let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
            let mp = (5 * doy + 2) / 153;
            let day = doy - (153 * mp + 2) / 5 + 1;
            let month = if mp < 10 { mp + 3 } else { mp - 9 };
            let year = yoe + era * 400 + i64::from(month <= 2);
            format!("{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z", s / 3600, s / 60 % 60, s % 60)
        };
        assert_eq!(parse_rfc3339(&at(started)), Some(started));
        let uid = my_uid().expect("id -u");
        let sleeper = |started_at: String| Process {
            pid,
            // As if someone else started it: the app doesn't offer to stop its own children.
            ppid: 1,
            uid,
            command: "sleep".into(),
            executable_path: "/bin/sleep".into(),
            started_at,
            ..Process::default()
        };
        let mut s = crate::app::init();
        s.me = Me { pid: u32::MAX, uid: Some(uid) };
        assert!(s.poke_rx.is_some(), "no feed thread runs in a test");

        s.land_procs(Ok(vec![sleeper(at(started - 1))]));
        s.ask(pid, Signal::Term);
        s.confirm();
        let status = s.outcome.as_ref().map(|o| o.status.clone());
        assert!(matches!(status, Some(crate::app::Status::Refused(_))), "{status:?}");
        assert!(child.0.try_wait().unwrap().is_none(), "a second off sends nothing");

        s.land_procs(Ok(vec![sleeper(at(started))]));
        s.ask(pid, Signal::Term);
        assert!(s.confirm.is_some());
        s.confirm();
        assert_eq!(s.outcome.as_ref().map(|o| o.status.clone()), Some(crate::app::Status::Asked { at: 0.0 }));
        let deadline = std::time::Instant::now() + Duration::from_secs(10);
        let status = loop {
            if let Some(s) = child.0.try_wait().unwrap() {
                break s;
            }
            assert!(std::time::Instant::now() < deadline, "sleep {pid} still running");
            std::thread::sleep(Duration::from_millis(20));
        };
        use std::os::unix::process::ExitStatusExt;
        assert_eq!(status.signal(), Some(15), "ended by SIGTERM");
        assert_eq!(System.identify(pid), Ok(None), "and ps no longer lists it");
    }
}
