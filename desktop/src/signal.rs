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

/// What `ps` says a pid is now: when it started (Unix seconds), its command, and whether it
/// has exited and only waits for its parent to collect it (state `Z`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Identity {
    pub started: i64,
    pub comm: String,
    pub zombie: bool,
}

/// Why a send didn't go through, and whether the signal may have been delivered anyway.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum SendError {
    /// `kill` didn't run, or ran and said no: nothing was sent.
    NotSent(String),
    /// `kill` ran but its answer was lost (it timed out, say): the signal may have gone.
    Unsure(String),
}

/// How the app asks `ps` about a pid and signals it. The real one runs `/bin/ps` and
/// `/bin/kill`; tests inject a recorder. Both run on a worker thread, never the UI's.
pub trait Signals: Send + Sync {
    /// `Ok(None)` when no process has `pid`.
    fn identify(&self, pid: u32) -> Result<Option<Identity>, String>;
    fn send(&self, pid: u32, signal: Signal) -> Result<(), SendError>;
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

/// How far up a parent chain [`refusal`] looks for the app; a reading with a loop in it stops
/// here rather than spinning.
const MAX_ANCESTORS: usize = 64;

/// Whether the app is among `p`'s ancestors, by `parent_of` (a pid's ppid in the current
/// reading). An ancestor missing from the reading ends the walk: a reading's grandchild whose
/// parent already exited and was reparented is no longer the app's.
fn started_by(p: &Process, me: u32, parent_of: &dyn Fn(u32) -> Option<u32>) -> bool {
    let mut ppid = p.ppid;
    for _ in 0..MAX_ANCESTORS {
        if ppid == me {
            return true;
        }
        if ppid <= 1 {
            return false;
        }
        match parent_of(ppid) {
            Some(next) => ppid = next,
            None => return false,
        }
    }
    false
}

/// Why `p` can't be quit from here, or `None` when it can. `parent_of` gives a pid's ppid in
/// the current reading, to refuse what the app's readings started and what those started.
pub fn refusal(p: &Process, me: Me, parent_of: &dyn Fn(u32) -> Option<u32>) -> Option<String> {
    if p.pid == 0 {
        return Some("This is the kernel; it can't be stopped.".into());
    }
    if p.pid == 1 {
        return Some(format!(
            "{} starts and looks after everything else; Spectra Desktop won't stop it.",
            p.name()
        ));
    }
    if p.pid == me.pid {
        return Some("This is Spectra Desktop itself.".into());
    }
    if started_by(p, me.pid, parent_of) {
        return Some("Spectra Desktop started it to take a reading; it ends on its own.".into());
    }
    if let Some(uid) = me.uid
        && uid != 0
        && p.uid != uid
    {
        let owner = if p.user.is_empty() {
            format!("uid {}", p.uid)
        } else {
            p.user.clone()
        };
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
        Signal::Term => format!(
            "{} (pid {}) will be asked to close. Unsaved changes may be lost.",
            t.name, t.pid
        ),
        Signal::Kill => format!(
            "{} (pid {}) will be stopped immediately. Unsaved changes will be lost.",
            t.name, t.pid
        ),
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
    /// `kill` ran but how it went is unknown: the signal may have been sent.
    Unsure(String),
}

/// `ps -o lstart=` with `TZ=UTC0` and `LC_ALL=C` (`Fri Nov  7 08:53:10 2026`) as Unix seconds.
pub fn parse_lstart(s: &str) -> Option<i64> {
    let mut w = s.split_whitespace();
    let (_weekday, month, day, time, year) =
        (w.next()?, w.next()?, w.next()?, w.next()?, w.next()?);
    if w.next().is_some() {
        return None;
    }
    const MONTHS: [&str; 12] = [
        "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
    ];
    let month = MONTHS.iter().position(|m| m.eq_ignore_ascii_case(month))? + 1;
    let day: u32 = day.parse().ok()?;
    let year: u32 = year.parse().ok()?;
    let t: Vec<&str> = time.split(':').collect();
    if t.len() != 3
        || t.iter()
            .any(|p| p.len() != 2 || !p.bytes().all(|b| b.is_ascii_digit()))
        || !(1..=31).contains(&day)
    {
        return None;
    }
    parse_rfc3339(&format!(
        "{year:04}-{month:02}-{day:02}T{}:{}:{}Z",
        t[0], t[1], t[2]
    ))
}

/// One line of `ps -o lstart= -o stat= -o comm=`: five words of start time, the state, then the
/// command, which may hold spaces. A zombie's command is `<defunct>` on macOS.
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
    let rest = rest.trim_start();
    let end = rest.find(char::is_whitespace).unwrap_or(rest.len());
    let (stat, comm) = rest.split_at(end);
    if stat.is_empty() {
        return None;
    }
    Some(Identity {
        started,
        comm: comm.trim().to_owned(),
        zombie: stat.starts_with('Z'),
    })
}

/// Whether a Linux `/proc/<pid>/status` shows other threads still running. A process whose main
/// thread exited while others run is listed as `Z` but is alive, and a signal still reaches it.
pub fn still_has_threads(status: Option<&str>) -> bool {
    let threads = status.and_then(|s| s.lines().find_map(|l| l.strip_prefix("Threads:")));
    threads
        .and_then(|n| n.trim().parse::<u32>().ok())
        .is_some_and(|n| n > 1)
}

fn base(path: &str) -> &str {
    path.rsplit('/').find(|p| !p.is_empty()).unwrap_or(path)
}

/// Whether `ps`'s command is the program the feed listed: its executable's path, or the name
/// at the end of either (a login shell's leading `-` aside). Linux cuts `comm` to 15 bytes.
///
/// On macOS `ps -o comm` is the process's argv[0], which a program may rewrite (Chromium-style
/// helpers and some daemons do). Such a process fails this check and is refused with "now runs
/// …": a false refusal, never a signal to the wrong process.
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
    let names = [
        (!exe.is_empty()).then(|| base(exe)),
        (!command.is_empty()).then_some(command),
    ];
    names
        .into_iter()
        .flatten()
        .any(|n| n == theirs || (theirs.len() == 15 && n.starts_with(theirs)))
}

/// Whether `now` (what `ps` says has the pid) is still `t`, or why not.
pub fn verify(t: &Target, now: Option<&Identity>) -> Result<(), String> {
    let Some(now) = now else {
        return Err("That process has already ended. Nothing was sent.".into());
    };
    let Some(started) = parse_rfc3339(&t.started) else {
        return Err(format!(
            "Spectra didn't say when {} started, so nothing was sent.",
            t.name
        ));
    };
    if now.started != started {
        return Err("That process has already ended; its pid now belongs to another process. Nothing was sent.".into());
    }
    if now.zombie {
        return Err(format!(
            "{} has already exited and is waiting for its parent to collect it; a signal can't help. Nothing was sent.",
            t.name
        ));
    }
    if !same_program(&now.comm, &t.process) {
        return Err(format!(
            "pid {} now runs {}, not {}. Nothing was sent.",
            t.pid, now.comm, t.name
        ));
    }
    Ok(())
}

/// Checks that `t`'s pid is still `t`, then sends `signal`, and nothing if the check fails.
pub fn quit(signals: &dyn Signals, t: &Target, signal: Signal) -> Attempt {
    let now = match signals.identify(t.pid) {
        Ok(now) => now,
        Err(e) => {
            return Attempt::Failed(format!(
                "Couldn't check that pid {} is still {}: {e}. Nothing was sent.",
                t.pid, t.name
            ));
        }
    };
    if let Err(why) = verify(t, now.as_ref()) {
        return Attempt::Refused(why);
    }
    match signals.send(t.pid, signal) {
        Ok(()) => Attempt::Sent,
        Err(SendError::NotSent(e)) => Attempt::Failed(format!(
            "Couldn't {} {}: {e}.",
            signal.verb().to_lowercase(),
            t.name
        )),
        Err(SendError::Unsure(e)) => Attempt::Unsure(e),
    }
}

/// Whether `now` is `t` exited and waiting for its parent: the same start, in state `Z`.
pub fn waits_for_parent(t: &Target, now: Option<&Identity>) -> bool {
    now.is_some_and(|now| now.zombie && parse_rfc3339(&t.started) == Some(now.started))
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

    fn send(&self, _: u32, _: Signal) -> Result<(), SendError> {
        Err(SendError::NotSent("this is sample data".into()))
    }
}

/// The user the app runs as, from `id -u`.
pub fn my_uid() -> Option<i64> {
    let mut cmd = Command::new("/usr/bin/id");
    cmd.arg("-u");
    let out = run::run(cmd, DEADLINE, MAX_OUTPUT)
        .ok()
        .filter(|o| o.status.success())?;
    String::from_utf8_lossy(&out.stdout).trim().parse().ok()
}

/// The real thing: `/bin/ps` and `/bin/kill`, each with a deadline.
pub struct System;

fn failure(what: &str, e: &RunError) -> String {
    match e {
        RunError::Spawn(e) => format!("couldn't run {what}: {e}"),
        RunError::Timeout(d) => format!("{what} took longer than {} s", d.as_secs()),
        RunError::TooLarge(_) => format!("{what} printed too much"),
        RunError::Io(e) => format!("couldn't read {what}'s output: {e}"),
    }
}

/// A `kill` that never started sent nothing; one that ran and then timed out or lost its output
/// may have delivered the signal before it did.
fn send_failure(e: RunError) -> SendError {
    let why = failure("kill", &e);
    match e {
        RunError::Spawn(_) => SendError::NotSent(why),
        RunError::Timeout(_) | RunError::TooLarge(_) | RunError::Io(_) => SendError::Unsure(why),
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
        cmd.env("TZ", "UTC0").env("LC_ALL", "C").args([
            "-o",
            "lstart=",
            "-o",
            "stat=",
            "-o",
            "comm=",
            "-p",
            &pid.to_string(),
        ]);
        let out = run::run(cmd, DEADLINE, MAX_OUTPUT).map_err(|e| failure("ps", &e))?;
        let stdout = String::from_utf8_lossy(&out.stdout);
        let Some(line) = stdout.lines().find(|l| !l.trim().is_empty()) else {
            // `ps -p` prints nothing and exits 1 for a pid nobody has.
            return if out.status.code() == Some(1) && out.stderr.iter().all(u8::is_ascii_whitespace)
            {
                Ok(None)
            } else {
                Err(format!(
                    "ps failed: {}",
                    String::from_utf8_lossy(&out.stderr).trim()
                ))
            };
        };
        let mut id =
            parse_ps(line).ok_or_else(|| format!("couldn't read ps's answer: {}", line.trim()))?;
        if cfg!(target_os = "linux") && id.zombie {
            let status = std::fs::read_to_string(format!("/proc/{pid}/status")).ok();
            id.zombie = !still_has_threads(status.as_deref());
        }
        Ok(Some(id))
    }

    fn send(&self, pid: u32, signal: Signal) -> Result<(), SendError> {
        if !signalable(pid) {
            return Err(SendError::NotSent(format!("refused to signal pid {pid}")));
        }
        let mut cmd = Command::new("/bin/kill");
        cmd.args([signal.flag(), &pid.to_string()]);
        let out = run::run(cmd, DEADLINE, MAX_OUTPUT).map_err(send_failure)?;
        if out.status.success() {
            return Ok(());
        }
        let why = String::from_utf8_lossy(&out.stderr);
        // `kill: 900: Operation not permitted` → the reason alone.
        let why = why
            .trim()
            .rsplit(": ")
            .next()
            .filter(|w| !w.is_empty())
            .unwrap_or("no reason given");
        Err(SendError::NotSent(format!("kill failed: {why}")))
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::sync::{Arc, Mutex};

    #[test]
    fn a_zombie_leader_with_running_threads_is_not_a_zombie() {
        assert!(still_has_threads(Some(
            "Name:\tjava\nState:\tZ (zombie)\nThreads:\t12\nVmRSS:\t1 kB\n"
        )));
        // A true zombie has no threads left but itself.
        assert!(!still_has_threads(Some(
            "State:\tZ (zombie)\nThreads:\t1\n"
        )));
        // Unreadable or unparseable: stay with what `ps` said.
        assert!(!still_has_threads(None));
        assert!(!still_has_threads(Some("State:\tZ (zombie)\n")));
        assert!(!still_has_threads(Some("Threads:\tmany\n")));
    }

    #[test]
    fn only_ordinary_pids_can_reach_kill() {
        for pid in [0, 1, 2_147_483_648, u32::MAX] {
            assert!(!signalable(pid), "{pid}");
            assert!(System.send(pid, Signal::Term).is_err(), "{pid}");
        }
        assert!(signalable(2) && signalable(i32::MAX as u32));
    }

    /// Answers `identify` from a table and records every signal instead of sending it. Shared
    /// with the worker that runs the checks, so behind locks.
    #[derive(Clone, Default)]
    pub struct Recorder {
        pub alive: Arc<Mutex<Vec<(u32, Identity)>>>,
        pub sent: Arc<Mutex<Vec<(u32, Signal)>>>,
        pub fail_kill: Arc<Mutex<Option<SendError>>>,
        pub fail_ps: Arc<Mutex<Option<String>>>,
        pub asked: Arc<Mutex<Vec<u32>>>,
    }

    impl Recorder {
        pub fn alive(&self, pid: u32, started_at: &str, comm: &str) {
            let started = parse_rfc3339(started_at).expect("an RFC 3339 time");
            self.alive.lock().unwrap().push((
                pid,
                Identity {
                    started,
                    comm: comm.into(),
                    zombie: false,
                },
            ));
        }

        /// `pid` is still listed by `ps`, but exited: state `Z`, command `<defunct>`.
        pub fn zombie(&self, pid: u32, started_at: &str) {
            let started = parse_rfc3339(started_at).expect("an RFC 3339 time");
            let mut alive = self.alive.lock().unwrap();
            alive.retain(|(p, _)| *p != pid);
            alive.push((
                pid,
                Identity {
                    started,
                    comm: "<defunct>".into(),
                    zombie: true,
                },
            ));
        }

        pub fn forget(&self, pid: u32) {
            self.alive.lock().unwrap().retain(|(p, _)| *p != pid);
        }

        pub fn sent(&self) -> Vec<(u32, Signal)> {
            self.sent.lock().unwrap().clone()
        }

        pub fn asked(&self) -> Vec<u32> {
            self.asked.lock().unwrap().clone()
        }

        pub fn fail_kill(&self, e: SendError) {
            *self.fail_kill.lock().unwrap() = Some(e);
        }

        pub fn fail_ps(&self, e: &str) {
            *self.fail_ps.lock().unwrap() = Some(e.into());
        }
    }

    impl Signals for Recorder {
        fn identify(&self, pid: u32) -> Result<Option<Identity>, String> {
            self.asked.lock().unwrap().push(pid);
            if let Some(e) = self.fail_ps.lock().unwrap().clone() {
                return Err(e);
            }
            Ok(self
                .alive
                .lock()
                .unwrap()
                .iter()
                .find(|(p, _)| *p == pid)
                .map(|(_, id)| id.clone()))
        }

        fn send(&self, pid: u32, signal: Signal) -> Result<(), SendError> {
            if let Some(e) = self.fail_kill.lock().unwrap().clone() {
                return Err(e);
            }
            self.sent.lock().unwrap().push((pid, signal));
            Ok(())
        }
    }

    fn slack() -> Process {
        Process {
            pid: 900,
            ppid: 1,
            uid: 501,
            user: "user".into(),
            command: "Slack".into(),
            executable_path: "/Applications/Slack.app/Contents/MacOS/Slack".into(),
            started_at: "2026-11-19T19:28:38-05:00".into(),
            role: Role::App,
            ..Process::default()
        }
    }

    const ME: Me = Me {
        pid: 5000,
        uid: Some(501),
    };

    #[test]
    fn reads_lstart_in_utc_with_single_digit_days() {
        assert_eq!(
            parse_lstart("Fri Nov 20 00:28:38 2026"),
            parse_rfc3339("2026-11-19T19:28:38-05:00")
        );
        // A one-digit day is padded with a space.
        assert_eq!(
            parse_lstart("Fri Nov  7 08:05:01 2026"),
            parse_rfc3339("2026-11-07T08:05:01Z")
        );
        assert_eq!(
            parse_lstart("Sat Feb 29 23:59:59 2028"),
            parse_rfc3339("2028-03-01T05:29:59+05:30")
        );
        assert_eq!(parse_lstart("Thu Jan  1 00:00:00 1970"), Some(0));
        for bad in [
            "",
            "Fri Nov 20 2026",
            "Fri Nov 20 0:28:38 2026",
            "Fri Nvb 20 00:28:38 2026",
            "Fri Nov 32 00:28:38 2026",
            "Fri Nov 20 00:28:38 2026 x",
        ] {
            assert_eq!(parse_lstart(bad), None, "{bad:?}");
        }
    }

    #[test]
    fn a_ps_line_keeps_a_command_with_spaces() {
        let id = parse_ps("Fri Nov  7 08:05:01 2026     S    /Applications/Slack.app/Contents/Frameworks/Slack Helper (GPU).app/Contents/MacOS/Slack Helper (GPU)\n").unwrap();
        assert_eq!(id.started, parse_rfc3339("2026-11-07T08:05:01Z").unwrap());
        assert_eq!(
            id.comm,
            "/Applications/Slack.app/Contents/Frameworks/Slack Helper (GPU).app/Contents/MacOS/Slack Helper (GPU)"
        );
        assert!(!id.zombie);
        assert_eq!(
            parse_ps("Fri Nov 7 08:05:01 2026 Ss+ sleep").unwrap().comm,
            "sleep"
        );
        assert_eq!(parse_ps("Fri Nov 7 08:05:01"), None);
        assert_eq!(parse_ps("Fri Nov 7 08:05:01 2026"), None, "no state");
        // As macOS prints a zombie.
        let z = parse_ps("Fri Nov 20 10:17:39 2026     ZN   <defunct>").unwrap();
        assert!(z.zombie);
        assert_eq!(z.comm, "<defunct>");
    }

    #[test]
    fn the_same_second_in_another_zone_matches_and_the_next_second_does_not() {
        let t = Target::of(&slack());
        let exe = "/Applications/Slack.app/Contents/MacOS/Slack";
        let at = |lstart: &str| Identity {
            started: parse_lstart(lstart).unwrap(),
            comm: exe.into(),
            zombie: false,
        };
        assert_eq!(
            verify(&t, Some(&at("Fri Nov 20 00:28:38 2026"))),
            Ok(()),
            "-05:00 is 00:28:38 UTC"
        );
        let mut ist = t.clone();
        ist.started = "2026-11-20T05:58:38.900+05:30".into();
        assert_eq!(
            verify(&ist, Some(&at("Fri Nov 20 00:28:38 2026"))),
            Ok(()),
            "fractions are a format, not a second"
        );
        assert!(
            verify(&t, Some(&at("Fri Nov 20 00:28:39 2026")))
                .unwrap_err()
                .contains("already ended")
        );
        assert!(
            verify(&t, Some(&at("Thu Nov 19 19:28:38 2026"))).is_err(),
            "local time read as UTC is a different instant"
        );
        assert_eq!(
            verify(&t, None).unwrap_err(),
            "That process has already ended. Nothing was sent."
        );
    }

    #[test]
    fn the_program_must_match_too() {
        let p = slack();
        assert!(same_program(
            "/Applications/Slack.app/Contents/MacOS/Slack",
            &p
        ));
        assert!(same_program("Slack", &p), "a bare name");
        assert!(!same_program("/usr/bin/node", &p));
        let mut zsh = Process {
            command: "zsh".into(),
            executable_path: "/bin/zsh".into(),
            ..Process::default()
        };
        assert!(same_program("-zsh", &zsh), "a login shell");
        zsh.command = "deploy.sh".into();
        zsh.executable_path = "/bin/bash".into();
        assert!(same_program("/bin/bash", &zsh));
        let long = Process {
            command: "qemu-system-aarch64-headless".into(),
            ..Process::default()
        };
        assert!(
            same_program("qemu-system-aar", &long),
            "Linux's 15-byte comm"
        );
        assert!(!same_program("qemu-system-x86", &long));
        let t = Target::of(&slack());
        let id = Identity {
            started: parse_rfc3339(&t.started).unwrap(),
            comm: "/usr/bin/node".into(),
            zombie: false,
        };
        assert_eq!(
            verify(&t, Some(&id)).unwrap_err(),
            "pid 900 now runs /usr/bin/node, not Slack. Nothing was sent."
        );
    }

    #[test]
    fn quit_sends_one_signal_only_after_the_check() {
        let r = Recorder::default();
        let t = Target::of(&slack());
        assert!(
            matches!(quit(&r, &t, Signal::Term), Attempt::Refused(_)),
            "gone"
        );
        assert!(r.sent().is_empty());
        r.alive(
            900,
            "2026-11-20T00:28:38Z",
            "/Applications/Slack.app/Contents/MacOS/Slack",
        );
        assert_eq!(quit(&r, &t, Signal::Kill), Attempt::Sent);
        assert_eq!(r.sent(), [(900, Signal::Kill)]);
        r.fail_kill(SendError::Unsure("kill took longer than 5 s".into()));
        assert_eq!(
            quit(&r, &t, Signal::Term),
            Attempt::Unsure("kill took longer than 5 s".into())
        );
        r.fail_kill(SendError::NotSent(
            "kill failed: Operation not permitted".into(),
        ));
        assert!(matches!(quit(&r, &t, Signal::Term), Attempt::Failed(_)));
        r.fail_ps("ps took longer than 5 s");
        let Attempt::Failed(why) = quit(&r, &t, Signal::Term) else {
            panic!()
        };
        assert!(why.contains("Nothing was sent"), "{why}");
        assert_eq!(r.sent().len(), 1);
    }

    #[test]
    fn a_zombie_gets_nothing_and_is_said_to_wait_for_its_parent() {
        let r = Recorder::default();
        let t = Target::of(&slack());
        r.zombie(900, "2026-11-20T00:28:38Z");
        let Attempt::Refused(why) = quit(&r, &t, Signal::Kill) else {
            panic!()
        };
        assert!(why.contains("waiting for its parent"), "{why}");
        assert!(r.sent().is_empty());
        let now = r.identify(900).unwrap();
        assert!(waits_for_parent(&t, now.as_ref()));
        // A zombie with another start is someone else's.
        r.zombie(900, "2026-11-20T00:28:39Z");
        assert!(!waits_for_parent(&t, r.identify(900).unwrap().as_ref()));
        assert!(!waits_for_parent(&t, None));
    }

    #[test]
    fn a_kill_that_never_ran_sent_nothing_and_one_that_hung_may_have() {
        assert!(matches!(
            send_failure(RunError::Spawn("no such file".into())),
            SendError::NotSent(_)
        ));
        assert_eq!(
            send_failure(RunError::Timeout(DEADLINE)),
            SendError::Unsure("kill took longer than 5 s".into())
        );
        assert!(matches!(
            send_failure(RunError::Io("broken pipe".into())),
            SendError::Unsure(_)
        ));
    }

    #[test]
    fn refuses_the_kernel_launchd_itself_its_readings_other_users_and_unknown_starts() {
        let none = |_: u32| None;
        let ok = slack();
        assert_eq!(refusal(&ok, ME, &none), None);
        let with = |f: &dyn Fn(&mut Process)| {
            let mut p = slack();
            f(&mut p);
            refusal(&p, ME, &none)
        };
        assert!(with(&|p| p.pid = 0).unwrap().contains("kernel"));
        assert!(with(&|p| p.pid = 1).unwrap().contains("won't stop it"));
        assert_eq!(
            with(&|p| p.pid = ME.pid).as_deref(),
            Some("This is Spectra Desktop itself.")
        );
        assert!(
            with(&|p| p.ppid = ME.pid)
                .unwrap()
                .contains("to take a reading")
        );
        assert_eq!(
            with(&|p| {
                p.uid = 0;
                p.user = "root".into()
            })
            .as_deref(),
            Some("Owned by root; Spectra Desktop can't stop it")
        );
        assert_eq!(
            with(&|p| {
                p.uid = -2;
                p.user.clear()
            })
            .as_deref(),
            Some("Owned by uid -2; Spectra Desktop can't stop it")
        );
        assert!(
            with(&|p| p.started_at.clear())
                .unwrap()
                .contains("didn't say when it started")
        );
        assert!(with(&|p| p.started_at = "yesterday".into()).is_some());
        // Its parent is not refused.
        assert_eq!(with(&|p| p.pid = 4999), None);
        // Run as root, it may stop anyone's; unsure who it is, the kill says.
        let mut root = slack();
        root.uid = 0;
        assert_eq!(
            refusal(
                &root,
                Me {
                    pid: 5000,
                    uid: Some(0)
                },
                &none
            ),
            None
        );
        assert_eq!(
            refusal(
                &root,
                Me {
                    pid: 5000,
                    uid: None
                },
                &none
            ),
            None
        );
    }

    #[test]
    fn what_the_apps_readings_started_is_refused_at_any_depth() {
        // 5000 (the app) → 5001 (spectra) → 5002 (ps) → 5003; 4000 → 4001 is someone else's.
        let parents = [
            (5001, ME.pid),
            (5002, 5001),
            (5003, 5002),
            (4001, 4000),
            (4000, 1),
        ];
        let parent_of = |pid: u32| parents.iter().find(|(p, _)| *p == pid).map(|(_, pp)| *pp);
        let child = |ppid: u32| Process { ppid, ..slack() };
        for ppid in [ME.pid, 5001, 5002, 5003] {
            assert!(
                refusal(&child(ppid), ME, &parent_of)
                    .unwrap()
                    .contains("to take a reading"),
                "under {ppid}"
            );
        }
        assert_eq!(refusal(&child(4001), ME, &parent_of), None);
        // An ancestor gone from the reading ends the walk; a loop in a reading doesn't spin.
        assert_eq!(refusal(&child(6000), ME, &parent_of), None);
        let looped = |pid: u32| Some(if pid == 7000 { 7001 } else { 7000 });
        assert_eq!(refusal(&child(7000), ME, &looped), None);
    }

    #[test]
    fn the_confirmation_says_what_will_happen_in_plain_words() {
        let t = Target::of(&slack());
        assert_eq!(
            consequence(&t, Signal::Term),
            ["Slack (pid 900) will be asked to close. Unsaved changes may be lost."]
        );
        assert_eq!(
            consequence(&t, Signal::Kill),
            ["Slack (pid 900) will be stopped immediately. Unsaved changes will be lost."]
        );
        let mut sys = slack();
        sys.role = Role::System;
        sys.command = "trustd".into();
        let lines = consequence(&Target::of(&sys), Signal::Term);
        assert_eq!(
            lines[1],
            "This is part of macOS and may restart or disrupt your session."
        );
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
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs() as i64
    }

    /// Spawns `prog` as this test's own child and reads its start from `ps`, which is where the
    /// feed gets it too. Linux works `lstart` out from boot time and clock ticks, so it may sit
    /// a second either side of the wall clock: only that much slack is allowed.
    fn spawn_own(prog: &str, arg: &str) -> (Child, i64) {
        let before = unix_now();
        let c = Child(Command::new(prog).arg(arg).spawn().expect("it runs"));
        let after = unix_now();
        let id = System
            .identify(c.0.id())
            .expect("ps runs")
            .expect("ps lists our child");
        assert!(
            (before - 1..=after + 1).contains(&id.started),
            "{} not in {before}..={after}",
            id.started
        );
        (c, id.started)
    }

    /// Seconds as an RFC 3339 UTC time.
    fn rfc3339(secs: i64) -> String {
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
        format!(
            "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
            s / 3600,
            s / 60 % 60,
            s % 60
        )
    }

    /// Lands what the worker threads finish, until `done` or ten seconds pass.
    fn wait_for(s: &mut crate::app::App, done: impl Fn(&crate::app::App) -> bool) {
        let deadline = std::time::Instant::now() + Duration::from_secs(10);
        while !done(s) {
            s.land_work();
            assert!(
                std::time::Instant::now() < deadline,
                "the worker never answered"
            );
            std::thread::sleep(Duration::from_millis(10));
        }
    }

    /// The app's own path (a reading, Quit, the confirmation, the worker thread) with the real
    /// `ps` and `kill`, on a `sleep` this test started and still holds, so its pid can't pass to
    /// another process: a start time a second off sends nothing, the right one quits it.
    #[test]
    fn quits_its_own_sleep_child_through_ps_and_kill() {
        let (mut child, started) = spawn_own("/bin/sleep", "600");
        let pid = child.0.id();
        assert_eq!(parse_rfc3339(&rfc3339(started)), Some(started));
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
        s.me = Me {
            pid: u32::MAX,
            uid: Some(uid),
        };
        assert!(s.poke_rx.is_some(), "no feed thread runs in a test");
        assert!(
            matches!(s.runner, crate::app::Runner::Thread),
            "the real worker"
        );
        let settled = |s: &crate::app::App| {
            s.outcome
                .as_ref()
                .is_some_and(|o| o.status != crate::app::Status::Checking)
        };

        s.land_procs(Ok(vec![sleeper(rfc3339(started - 1))]));
        s.ask(pid, Signal::Term);
        s.confirm();
        wait_for(&mut s, settled);
        let status = s.outcome.as_ref().map(|o| o.status.clone());
        assert!(
            matches!(status, Some(crate::app::Status::Refused(_))),
            "{status:?}"
        );
        assert!(
            child.0.try_wait().unwrap().is_none(),
            "a second off sends nothing"
        );

        s.land_procs(Ok(vec![sleeper(rfc3339(started))]));
        s.ask(pid, Signal::Term);
        assert!(s.confirm.is_some());
        s.confirm();
        wait_for(&mut s, settled);
        assert_eq!(
            s.outcome.as_ref().map(|o| o.status.clone()),
            Some(crate::app::Status::Asked { at: 0.0 })
        );
        let deadline = std::time::Instant::now() + Duration::from_secs(10);
        let status = loop {
            if let Some(s) = child.0.try_wait().unwrap() {
                break s;
            }
            assert!(
                std::time::Instant::now() < deadline,
                "sleep {pid} still running"
            );
            std::thread::sleep(Duration::from_millis(20));
        };
        use std::os::unix::process::ExitStatusExt;
        assert_eq!(status.signal(), Some(15), "ended by SIGTERM");
        assert_eq!(System.identify(pid), Ok(None), "and ps no longer lists it");
    }

    /// A child of this test that has exited but isn't collected yet is what `ps` calls a zombie;
    /// nothing is signalled.
    #[test]
    fn ps_says_when_its_own_exited_child_waits_for_it() {
        let (child, started) = spawn_own("/bin/sleep", "0");
        let pid = child.0.id();
        let deadline = std::time::Instant::now() + Duration::from_secs(10);
        let id = loop {
            let id = System
                .identify(pid)
                .expect("ps runs")
                .expect("unreaped, so still listed");
            if id.zombie {
                break id;
            }
            assert!(std::time::Instant::now() < deadline, "sleep 0 never exited");
            std::thread::sleep(Duration::from_millis(20));
        };
        assert_eq!(id.started, started, "a zombie keeps its start");
        let t = Target::of(&Process {
            pid,
            command: "sleep".into(),
            executable_path: "/bin/sleep".into(),
            started_at: rfc3339(started),
            ..Process::default()
        });
        assert!(waits_for_parent(&t, Some(&id)));
        assert!(
            verify(&t, Some(&id))
                .unwrap_err()
                .contains("waiting for its parent")
        );
        drop(child);
    }
}
