//! Runs a feed command with a deadline and a cap on what it may print, so a Spectra that hangs
//! or floods its pipe can't freeze the numbers.

use std::io::Read;
use std::os::unix::process::CommandExt;
use std::process::{Child, Command, ExitStatus, Stdio};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::mpsc::{Receiver, channel};
use std::time::{Duration, Instant};

/// How often a running command is checked on at first; the wait doubles up to [`CHECK_MAX`],
/// so a 17 s reading wakes its thread about 90 times instead of 700.
const CHECK: Duration = Duration::from_millis(25);
const CHECK_MAX: Duration = Duration::from_millis(200);

/// Commands started here that haven't finished stopping or exiting.
static RUNNING: AtomicUsize = AtomicUsize::new(0);

/// Counts one run in [`RUNNING`] for as long as it lives.
struct Running;

impl Running {
    fn new() -> Running {
        RUNNING.fetch_add(1, Ordering::SeqCst);
        Running
    }
}

impl Drop for Running {
    fn drop(&mut self) {
        RUNNING.fetch_sub(1, Ordering::SeqCst);
    }
}

/// Work that will run a command (or another after its first) but may not have started it:
/// [`wait_idle`] waits for it from the moment it's queued until it's dropped.
pub struct Pending {
    _running: Running,
}

/// Holds quit until the returned [`Pending`] is dropped.
pub fn pending() -> Pending {
    Pending {
        _running: Running::new(),
    }
}

/// Waits until every run, and every [`Pending`] job, has finished (a cancelled one finishes within a check or two), or
/// `limit` passes. True when none is left. Used at quit, so cancelled jobs are killed before the
/// process exits.
pub fn wait_idle(limit: Duration) -> bool {
    let start = Instant::now();
    while RUNNING.load(Ordering::SeqCst) > 0 {
        if start.elapsed() >= limit {
            return false;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    true
}

/// Kills the process group `child` leads, then `child` itself. The group exists only because
/// [`run_until`] made the child its leader (`process_group(0)`), so its id is the child's pid and
/// holds only what the child started. Must run before the child is reaped, while the pid can't
/// belong to anyone else.
fn kill_group(child: &mut Child) {
    let pgid = child.id();
    // Never a group-wide kill for pid 0 or 1, which would reach our own group or everything.
    if pgid > 1 && i32::try_from(pgid).is_ok() {
        let _ = Command::new("/bin/kill")
            .args(["-KILL", "--", &format!("-{pgid}")])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status();
    }
    let _ = child.kill();
}

#[derive(Debug)]
pub struct Output {
    pub status: ExitStatus,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

#[derive(Debug, PartialEq)]
pub enum RunError {
    /// It couldn't be started.
    Spawn(String),
    /// Still running at the deadline; it was killed.
    Timeout(Duration),
    /// It printed more than the cap; it was killed.
    TooLarge(usize),
    Io(String),
}

/// How a cancellable run ended without output.
#[derive(Debug, PartialEq)]
pub enum Ended {
    Failed(RunError),
    /// Asked to stop before it finished; it was killed.
    Cancelled,
}

/// The daemon's answer as the CLI's output: what it would have printed, exit status 0.
fn daemon_output(
    answer: Result<String, crate::daemon::Error>,
    deadline: Duration,
    cap: usize,
) -> Result<Output, Ended> {
    use std::os::unix::process::ExitStatusExt;
    match answer {
        Ok(s) if s.len() > cap => Err(Ended::Failed(RunError::TooLarge(cap))),
        Ok(s) => Ok(Output {
            status: ExitStatus::from_raw(0),
            stdout: s.into_bytes(),
            stderr: Vec::new(),
        }),
        Err(crate::daemon::Error::Cancelled) => Err(Ended::Cancelled),
        Err(crate::daemon::Error::Timeout) => Err(Ended::Failed(RunError::Timeout(deadline))),
        Err(e) => Err(Ended::Failed(RunError::Io(format!(
            "Spectra's daemon: {}",
            e.why()
        )))),
    }
}

/// The daemon's answer for `cmd` as the CLI's output, when the daemon takes it; `None` when the
/// call has to be made here instead.
pub fn ask_daemon(
    cmd: &Command,
    deadline: Duration,
    cap: usize,
    cancel: &AtomicBool,
) -> Option<Result<Output, Ended>> {
    let _running = Running::new();
    crate::daemon::answer(cmd, deadline, cancel).map(|a| daemon_output(a, deadline, cap))
}

/// Reads a pipe on its own thread until it closes, keeping at most `cap` bytes. Past the cap it
/// says so at once and keeps draining, so the child never blocks on a full pipe.
fn drain(mut pipe: impl Read + Send + 'static, cap: usize) -> Receiver<Result<Vec<u8>, RunError>> {
    let (tx, rx) = channel();
    std::thread::spawn(move || {
        let mut kept = Vec::new();
        let mut buf = [0u8; 64 * 1024];
        let mut over = false;
        loop {
            match pipe.read(&mut buf) {
                Ok(0) => break,
                Ok(n) if !over => {
                    if kept.len() + n > cap {
                        over = true;
                        kept = Vec::new();
                        let _ = tx.send(Err(RunError::TooLarge(cap)));
                    } else {
                        kept.extend_from_slice(&buf[..n]);
                    }
                }
                Ok(_) => {}
                Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
                Err(e) => {
                    let _ = tx.send(Err(RunError::Io(e.to_string())));
                    return;
                }
            }
        }
        if !over {
            let _ = tx.send(Ok(kept));
        }
    });
    rx
}

/// Runs `cmd` to completion, or kills it once `deadline` passes or it prints more than `cap`
/// bytes to either pipe.
pub fn run(cmd: Command, deadline: Duration, cap: usize) -> Result<Output, RunError> {
    // Nothing sets this flag, so the run is never cancelled.
    run_until(cmd, deadline, cap, &AtomicBool::new(false)).map_err(|e| match e {
        Ended::Failed(e) => e,
        Ended::Cancelled => RunError::Io("stopped".into()),
    })
}

/// [`run`], also killing it once `cancel` is set. The command runs in a process group of its
/// own, and stopping it stops that group: its children go too, and nothing else.
pub fn run_until(
    mut cmd: Command,
    deadline: Duration,
    cap: usize,
    cancel: &AtomicBool,
) -> Result<Output, Ended> {
    let _running = Running::new();
    let start = Instant::now();
    // A call Spectra's daemon answers starts no process.
    if let Some(out) = ask_daemon(&cmd, deadline, cap, cancel) {
        return out;
    }
    // Time spent waiting for a daemon that was connecting counts against the same deadline.
    if start.elapsed() >= deadline {
        return Err(Ended::Failed(RunError::Timeout(deadline)));
    }
    let mut child = cmd
        .process_group(0)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| Ended::Failed(RunError::Spawn(e.to_string())))?;
    let out = drain(child.stdout.take().expect("piped stdout"), cap);
    let err = drain(child.stderr.take().expect("piped stderr"), cap);
    let mut check = CHECK;
    let (mut stdout, mut stderr) = (None, None);
    let stop = |child: &mut std::process::Child, why: Ended| {
        kill_group(child);
        let _ = child.wait();
        Err(why)
    };
    loop {
        // A pipe that overflowed ends the run without waiting for the child.
        for (rx, slot) in [(&out, &mut stdout), (&err, &mut stderr)] {
            if slot.is_none() {
                match rx.try_recv() {
                    Ok(Ok(bytes)) => *slot = Some(bytes),
                    Ok(Err(e)) => return stop(&mut child, Ended::Failed(e)),
                    Err(_) => {}
                }
            }
        }
        match child.try_wait() {
            Ok(Some(status)) => {
                // Exited: the pipes close unless a grandchild still holds them, so wait for
                // the readers only as long as the deadline allows.
                let left = deadline.saturating_sub(start.elapsed());
                let take =
                    |rx: &Receiver<Result<Vec<u8>, RunError>>, slot: Option<Vec<u8>>| match slot {
                        Some(b) => Ok(b),
                        None => rx
                            .recv_timeout(left)
                            .map_err(|_| RunError::Timeout(deadline))?,
                    };
                let stdout = take(&out, stdout).map_err(Ended::Failed)?;
                let stderr = take(&err, stderr).map_err(Ended::Failed)?;
                return Ok(Output {
                    status,
                    stdout,
                    stderr,
                });
            }
            Ok(None) if cancel.load(Ordering::Relaxed) => {
                return stop(&mut child, Ended::Cancelled);
            }
            Ok(None) if start.elapsed() >= deadline => {
                return stop(&mut child, Ended::Failed(RunError::Timeout(deadline)));
            }
            Ok(None) => {
                std::thread::sleep(check);
                check = (check * 2).min(CHECK_MAX);
            }
            Err(e) => return stop(&mut child, Ended::Failed(RunError::Io(e.to_string()))),
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use std::path::PathBuf;

    #[test]
    fn a_daemon_answer_reads_as_the_clis() {
        use crate::daemon::Error;
        let d = Duration::from_secs(1);
        let out = daemon_output(Ok("[1]".into()), d, 64).unwrap();
        assert!(out.status.success() && out.stdout == b"[1]");
        assert_eq!(
            daemon_output(Ok("x".repeat(65)), d, 64).unwrap_err(),
            Ended::Failed(RunError::TooLarge(64))
        );
        assert_eq!(
            daemon_output(Err(Error::Cancelled), d, 64).unwrap_err(),
            Ended::Cancelled
        );
        assert_eq!(
            daemon_output(Err(Error::Timeout), d, 64).unwrap_err(),
            Ended::Failed(RunError::Timeout(d))
        );
        // A classification that failed says why on the feed's "last reading failed" line.
        let Err(Ended::Failed(e)) =
            daemon_output(Err(Error::Warned("classify: timed out".into())), d, 64)
        else {
            panic!("a failure")
        };
        assert!(crate::procs::run_failure("spectra", e).contains("classify: timed out"));
    }

    /// A shell script standing in for Spectra, in its own temp directory.
    pub fn script(name: &str, body: &str) -> PathBuf {
        let dir =
            std::env::temp_dir().join(format!("spectra-desktop-{}-{name}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("spectra");
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
        path
    }

    #[test]
    fn quit_waits_for_a_pending_job_until_it_is_done() {
        let job = pending();
        assert!(
            !wait_idle(Duration::from_millis(30)),
            "queued but not started still counts"
        );
        let start = Instant::now();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(100));
            drop(job);
        });
        wait_idle(Duration::from_secs(2));
        assert!(start.elapsed() >= Duration::from_millis(90));
    }

    #[test]
    fn a_quick_command_returns_its_output() {
        let bin = script("quick", "echo out; echo err >&2; exit 3");
        let o = run(Command::new(&bin), Duration::from_secs(20), 1024).unwrap();
        assert_eq!(o.stdout, b"out\n");
        assert_eq!(o.stderr, b"err\n");
        assert_eq!(o.status.code(), Some(3));
    }

    #[test]
    fn a_command_past_its_deadline_is_killed() {
        // `exec` so the shell is the sleeper and killing it ends the sleep.
        let bin = script("hang", "exec sleep 60");
        let deadline = Duration::from_millis(200);
        let start = Instant::now();
        assert_eq!(
            run(Command::new(&bin), deadline, 1024).unwrap_err(),
            RunError::Timeout(deadline)
        );
        assert!(
            start.elapsed() < Duration::from_secs(30),
            "it didn't wait for the sleep"
        );
    }

    #[test]
    fn a_cancelled_command_is_killed() {
        let bin = script("cancel", "exec sleep 60");
        let cancel = std::sync::Arc::new(AtomicBool::new(false));
        let flag = std::sync::Arc::clone(&cancel);
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(100));
            flag.store(true, Ordering::Relaxed);
        });
        let start = Instant::now();
        assert_eq!(
            run_until(Command::new(&bin), Duration::from_secs(30), 1024, &cancel).unwrap_err(),
            Ended::Cancelled
        );
        assert!(
            start.elapsed() < Duration::from_secs(20),
            "it didn't wait for the sleep"
        );
    }

    /// Whether `pid` is alive and not a zombie. Only ever asked about pids this test started.
    fn alive(pid: u32) -> bool {
        let out = Command::new("/bin/ps")
            .args(["-o", "stat=", "-p", &pid.to_string()])
            .output()
            .unwrap();
        let stat = String::from_utf8_lossy(&out.stdout);
        let stat = stat.trim();
        !stat.is_empty() && !stat.starts_with('Z')
    }

    fn wait_gone(pid: u32) -> bool {
        let start = Instant::now();
        while alive(pid) && start.elapsed() < Duration::from_secs(5) {
            std::thread::sleep(Duration::from_millis(20));
        }
        !alive(pid)
    }

    /// A shell that starts a `sleep` grandchild and prints its pid, then waits on it.
    fn grandparent(name: &str) -> (PathBuf, PathBuf) {
        let pidfile =
            std::env::temp_dir().join(format!("spectra-desktop-{}-{name}.pid", std::process::id()));
        let _ = std::fs::remove_file(&pidfile);
        let body = format!("sleep 60 &\necho $! > {}\nwait", pidfile.display());
        (script(name, &body), pidfile)
    }

    fn grandchild_pid(pidfile: &std::path::Path) -> u32 {
        let start = Instant::now();
        loop {
            if let Some(pid) = std::fs::read_to_string(pidfile)
                .ok()
                .and_then(|t| t.trim().parse().ok())
            {
                return pid;
            }
            assert!(
                start.elapsed() < Duration::from_secs(5),
                "the shell never started its sleep"
            );
            std::thread::sleep(Duration::from_millis(10));
        }
    }

    #[test]
    fn cancelling_kills_the_grandchildren_too() {
        let (bin, pidfile) = grandparent("group-cancel");
        let cancel = std::sync::Arc::new(AtomicBool::new(false));
        let flag = std::sync::Arc::clone(&cancel);
        let file = pidfile.clone();
        let waiter = std::thread::spawn(move || {
            let gc = grandchild_pid(&file);
            assert!(alive(gc));
            flag.store(true, Ordering::Relaxed);
            gc
        });
        assert_eq!(
            run_until(Command::new(&bin), Duration::from_secs(30), 1024, &cancel).unwrap_err(),
            Ended::Cancelled
        );
        let gc = waiter.join().unwrap();
        assert!(
            wait_gone(gc),
            "the sleep started by the shell outlived the cancel"
        );
    }

    #[test]
    fn a_deadline_kills_the_grandchildren_too() {
        let (bin, pidfile) = grandparent("group-deadline");
        let deadline = Duration::from_millis(400);
        assert_eq!(
            run(Command::new(&bin), deadline, 1024).unwrap_err(),
            RunError::Timeout(deadline)
        );
        assert!(
            wait_gone(grandchild_pid(&pidfile)),
            "the sleep started by the shell outlived the deadline"
        );
    }

    #[test]
    fn a_flood_is_cut_off_at_the_cap() {
        let bin = script("flood", "exec yes spectra");
        assert_eq!(
            run(Command::new(&bin), Duration::from_secs(20), 4096).unwrap_err(),
            RunError::TooLarge(4096)
        );
    }
}
