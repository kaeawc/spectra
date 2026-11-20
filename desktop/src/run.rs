//! Runs a feed command with a deadline and a cap on what it may print, so a Spectra that hangs
//! or floods its pipe can't freeze the numbers.

use std::io::Read;
use std::process::{Command, ExitStatus, Stdio};
use std::sync::mpsc::{Receiver, channel};
use std::time::{Duration, Instant};

/// How often a running command is checked on.
const CHECK: Duration = Duration::from_millis(25);

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
pub fn run(mut cmd: Command, deadline: Duration, cap: usize) -> Result<Output, RunError> {
    let mut child = cmd
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| RunError::Spawn(e.to_string()))?;
    let out = drain(child.stdout.take().expect("piped stdout"), cap);
    let err = drain(child.stderr.take().expect("piped stderr"), cap);
    let start = Instant::now();
    let (mut stdout, mut stderr) = (None, None);
    let stop = |child: &mut std::process::Child, why: RunError| {
        let _ = child.kill();
        let _ = child.wait();
        Err(why)
    };
    loop {
        // A pipe that overflowed ends the run without waiting for the child.
        for (rx, slot) in [(&out, &mut stdout), (&err, &mut stderr)] {
            if slot.is_none() {
                match rx.try_recv() {
                    Ok(Ok(bytes)) => *slot = Some(bytes),
                    Ok(Err(e)) => return stop(&mut child, e),
                    Err(_) => {}
                }
            }
        }
        match child.try_wait() {
            Ok(Some(status)) => {
                // Exited: the pipes close unless a grandchild still holds them, so wait for
                // the readers only as long as the deadline allows.
                let left = deadline.saturating_sub(start.elapsed());
                let take =|rx: &Receiver<Result<Vec<u8>, RunError>>, slot: Option<Vec<u8>>| match slot {
                    Some(b) => Ok(b),
                    None => rx.recv_timeout(left).map_err(|_| RunError::Timeout(deadline))?,
                };
                let stdout = take(&out, stdout)?;
                let stderr = take(&err, stderr)?;
                return Ok(Output { status, stdout, stderr });
            }
            Ok(None) if start.elapsed() >= deadline => return stop(&mut child, RunError::Timeout(deadline)),
            Ok(None) => std::thread::sleep(CHECK),
            Err(e) => return stop(&mut child, RunError::Io(e.to_string())),
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use std::path::PathBuf;

    /// A shell script standing in for Spectra, in its own temp directory.
    pub fn script(name: &str, body: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("spectra-desktop-{}-{name}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("spectra");
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
        path
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
        assert_eq!(run(Command::new(&bin), deadline, 1024).unwrap_err(), RunError::Timeout(deadline));
        assert!(start.elapsed() < Duration::from_secs(30), "it didn't wait for the sleep");
    }

    #[test]
    fn a_flood_is_cut_off_at_the_cap() {
        let bin = script("flood", "exec yes spectra");
        assert_eq!(run(Command::new(&bin), Duration::from_secs(20), 4096).unwrap_err(), RunError::TooLarge(4096));
    }
}
