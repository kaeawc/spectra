//! Running other programs (Go's `internal/proc`). Collectors take `&dyn Runner` instead of
//! spawning processes, so tests answer with [`FakeRunner`] and the parity harness can replay
//! recorded output.

use std::fmt;
use std::io::{self, Read, Write};
use std::path::PathBuf;
use std::process::{Child, Stdio};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::thread::JoinHandle;
use std::time::Duration;

use crate::clock::lock;

/// The timeout [`Cmd::bounded`] applies, as Go's `proc.DefaultTimeout`.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(120);

/// A command to run.
#[derive(Clone, Debug, Default)]
pub struct Cmd {
    pub name: String,
    pub args: Vec<String>,
    pub dir: Option<PathBuf>,
    /// The whole environment for the child; `None` inherits this process's.
    pub env: Option<Vec<(String, String)>>,
    pub stdin: Vec<u8>,
    /// Kill the child and fail once it has run this long.
    pub timeout: Option<Duration>,
    /// Kill the child and fail once this is cancelled.
    pub cancel: Option<CancelToken>,
}

impl Cmd {
    pub fn new<S: Into<String>>(name: impl Into<String>, args: impl IntoIterator<Item = S>) -> Self {
        Cmd { name: name.into(), args: args.into_iter().map(Into::into).collect(), ..Cmd::default() }
    }

    /// With [`DEFAULT_TIMEOUT`], like Go's `BoundedOutput`.
    pub fn bounded(mut self) -> Self {
        self.timeout = Some(DEFAULT_TIMEOUT);
        self
    }

    pub fn timeout(mut self, d: Duration) -> Self {
        self.timeout = Some(d);
        self
    }

    pub fn env(mut self, env: Vec<(String, String)>) -> Self {
        self.env = Some(env);
        self
    }

    pub fn cancel(mut self, token: CancelToken) -> Self {
        self.cancel = Some(token);
        self
    }

    /// `name arg1 arg2`, for error messages.
    pub fn display(&self) -> String {
        std::iter::once(self.name.as_str()).chain(self.args.iter().map(String::as_str)).collect::<Vec<_>>().join(" ")
    }
}

/// What a finished command printed and how it exited. `exit_code` is -1 when it was killed by a
/// signal.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Output {
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
    pub exit_code: i32,
}

/// Why a command produced no [`Output`]. A non-zero exit is not an error here; see [`output`].
#[derive(Debug)]
pub enum RunError {
    /// The program could not be started (not found, not executable...).
    Spawn {
        cmd: String,
        source: io::Error,
    },
    TimedOut {
        cmd: String,
        after: Duration,
    },
    Cancelled {
        cmd: String,
    },
    /// Reading its output or waiting for it failed.
    Io {
        cmd: String,
        source: io::Error,
    },
    /// From [`output`]: it ran and exited non-zero.
    Exit {
        cmd: String,
        code: i32,
        stderr: String,
    },
    /// From [`FakeRunner`]: no rule matched.
    Unmatched {
        cmd: String,
    },
}

impl fmt::Display for RunError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            RunError::Spawn { cmd, source } => write!(f, "{cmd}: {source}"),
            RunError::TimedOut { cmd, after } => write!(f, "{cmd} timed out after {after:?}"),
            RunError::Cancelled { cmd } => write!(f, "{cmd}: cancelled"),
            RunError::Io { cmd, source } => write!(f, "{cmd}: {source}"),
            // Same text as Go's proc.Output.
            RunError::Exit { cmd, code, stderr } => write!(f, "{cmd} exited {code}: {stderr}"),
            RunError::Unmatched { cmd } => write!(f, "proc::FakeRunner: no rule matched {cmd}"),
        }
    }
}

impl std::error::Error for RunError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            RunError::Spawn { source, .. } | RunError::Io { source, .. } => Some(source),
            _ => None,
        }
    }
}

/// Runs commands.
pub trait Runner: Send + Sync {
    fn run(&self, cmd: &Cmd) -> Result<Output, RunError>;
}

/// Runs `cmd` and returns its stdout, failing on a non-zero exit (Go's `proc.Output`).
pub fn output(runner: &dyn Runner, cmd: &Cmd) -> Result<Vec<u8>, RunError> {
    let out = runner.run(cmd)?;
    if out.exit_code != 0 {
        let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
        return Err(RunError::Exit { cmd: cmd.display(), code: out.exit_code, stderr });
    }
    Ok(out.stdout)
}

/// A shared flag that stops a running command. Cloning shares the flag.
#[derive(Clone, Debug, Default)]
pub struct CancelToken(Arc<AtomicBool>);

impl CancelToken {
    pub fn new() -> Self {
        CancelToken::default()
    }

    pub fn cancel(&self) {
        self.0.store(true, Ordering::SeqCst);
    }

    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::SeqCst)
    }
}

/// Runs real processes.
#[derive(Clone, Copy, Debug, Default)]
pub struct OsRunner;

/// How often a running child is checked for exit, timeout or cancel.
const POLL: Duration = Duration::from_millis(5);

impl Runner for OsRunner {
    #[allow(clippy::disallowed_methods)] // the one place real processes are started and timed
    fn run(&self, cmd: &Cmd) -> Result<Output, RunError> {
        let mut c = std::process::Command::new(&cmd.name);
        c.args(&cmd.args).stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped());
        if let Some(dir) = &cmd.dir {
            c.current_dir(dir);
        }
        if let Some(env) = &cmd.env {
            c.env_clear().envs(env.iter().map(|(k, v)| (k, v)));
        }
        let mut child = c.spawn().map_err(|source| RunError::Spawn { cmd: cmd.display(), source })?;
        let stdin = feed_stdin(&mut child, cmd.stdin.clone());
        let stdout = drain(child.stdout.take());
        let stderr = drain(child.stderr.take());

        let started = std::time::Instant::now();
        let status = loop {
            match child.try_wait() {
                Ok(Some(status)) => break Ok(status),
                Ok(None) => {}
                Err(source) => break Err(RunError::Io { cmd: cmd.display(), source }),
            }
            if cmd.cancel.as_ref().is_some_and(CancelToken::is_cancelled) {
                break Err(RunError::Cancelled { cmd: cmd.display() });
            }
            if let Some(after) = cmd.timeout.filter(|t| started.elapsed() >= *t) {
                break Err(RunError::TimedOut { cmd: cmd.display(), after });
            }
            std::thread::sleep(POLL);
        };
        if status.is_err() {
            let _ = child.kill();
            let _ = child.wait();
        }
        let _ = stdin.map(JoinHandle::join);
        let stdout = join(stdout);
        let stderr = join(stderr);
        let status = status?;
        Ok(Output { stdout, stderr, exit_code: status.code().unwrap_or(-1) })
    }
}

fn feed_stdin(child: &mut Child, data: Vec<u8>) -> Option<JoinHandle<()>> {
    let mut pipe = child.stdin.take()?;
    if data.is_empty() {
        return None; // dropping the pipe closes the child's stdin
    }
    Some(std::thread::spawn(move || {
        let _ = pipe.write_all(&data);
    }))
}

fn drain<R: Read + Send + 'static>(pipe: Option<R>) -> Option<JoinHandle<Vec<u8>>> {
    let mut pipe = pipe?;
    Some(std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = pipe.read_to_end(&mut buf);
        buf
    }))
}

fn join(handle: Option<JoinHandle<Vec<u8>>>) -> Vec<u8> {
    handle.and_then(|h| h.join().ok()).unwrap_or_default()
}

/// Decides whether a [`FakeRunner`] rule answers a command.
pub type Matcher = Box<dyn Fn(&Cmd) -> bool + Send + Sync>;

/// Matches `name` with exactly `args`.
pub fn match_exact(name: &str, args: &[&str]) -> Matcher {
    let name = name.to_string();
    let args: Vec<String> = args.iter().map(|a| a.to_string()).collect();
    Box::new(move |c| c.name == name && c.args == args)
}

/// Matches `name` whose arguments start with `args`.
pub fn match_prefix(name: &str, args: &[&str]) -> Matcher {
    let name = name.to_string();
    let args: Vec<String> = args.iter().map(|a| a.to_string()).collect();
    Box::new(move |c| c.name == name && c.args.starts_with(&args))
}

/// A recorded call: the command as the collector built it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Call {
    pub name: String,
    pub args: Vec<String>,
    pub dir: Option<PathBuf>,
    pub env: Option<Vec<(String, String)>>,
}

/// Answers commands from rules, first match wins, and records every call.
#[derive(Default)]
pub struct FakeRunner {
    rules: std::sync::Mutex<Vec<(Matcher, Result<Output, String>)>>,
    calls: std::sync::Mutex<Vec<Call>>,
}

impl FakeRunner {
    pub fn new() -> Self {
        FakeRunner::default()
    }

    /// Commands that `matcher` accepts get `output`.
    pub fn on(&self, matcher: Matcher, output: Output) -> &Self {
        lock(&self.rules).push((matcher, Ok(output)));
        self
    }

    /// Commands that `matcher` accepts exit 0 with `stdout`.
    pub fn on_stdout(&self, matcher: Matcher, stdout: impl Into<Vec<u8>>) -> &Self {
        self.on(matcher, Output { stdout: stdout.into(), ..Output::default() })
    }

    /// Commands that `matcher` accepts fail to start with `message`.
    pub fn on_error(&self, matcher: Matcher, message: impl Into<String>) -> &Self {
        lock(&self.rules).push((matcher, Err(message.into())));
        self
    }

    pub fn calls(&self) -> Vec<Call> {
        lock(&self.calls).clone()
    }
}

impl Runner for FakeRunner {
    fn run(&self, cmd: &Cmd) -> Result<Output, RunError> {
        lock(&self.calls).push(Call { name: cmd.name.clone(), args: cmd.args.clone(), dir: cmd.dir.clone(), env: cmd.env.clone() });
        let rules = lock(&self.rules);
        match rules.iter().find(|(m, _)| m(cmd)) {
            Some((_, Ok(out))) => Ok(out.clone()),
            Some((_, Err(msg))) => Err(RunError::Spawn { cmd: cmd.display(), source: io::Error::other(msg.clone()) }),
            None => Err(RunError::Unmatched { cmd: cmd.display() }),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn os_runner_captures_output_and_exit_code() {
        let out = OsRunner.run(&Cmd::new("/bin/sh", ["-c", "printf out; printf err >&2; exit 3"])).expect("run");
        assert_eq!(out, Output { stdout: b"out".to_vec(), stderr: b"err".to_vec(), exit_code: 3 });
    }

    #[test]
    fn os_runner_feeds_stdin_and_env() {
        let mut cmd = Cmd::new("/bin/sh", ["-c", "cat; printf \"$SPECTRA_T\""]).env(vec![("SPECTRA_T".into(), "x".into())]);
        cmd.stdin = b"in-".to_vec();
        assert_eq!(OsRunner.run(&cmd).expect("run").stdout, b"in-x");
    }

    #[test]
    fn os_runner_times_out() {
        let err = OsRunner.run(&Cmd::new("/bin/sleep", ["5"]).timeout(Duration::from_millis(50))).expect_err("timeout");
        assert!(matches!(err, RunError::TimedOut { .. }), "{err}");
    }

    #[test]
    fn os_runner_cancels() {
        let token = CancelToken::new();
        token.cancel();
        let err = OsRunner.run(&Cmd::new("/bin/sleep", ["5"]).cancel(token)).expect_err("cancel");
        assert!(matches!(err, RunError::Cancelled { .. }), "{err}");
    }

    #[test]
    fn os_runner_reports_missing_program() {
        let err = OsRunner.run(&Cmd::new("/nonexistent/spectra-test", Vec::<String>::new())).expect_err("spawn");
        assert!(matches!(err, RunError::Spawn { .. }));
    }

    #[test]
    fn output_fails_on_nonzero_exit_like_go() {
        let fake = FakeRunner::new();
        fake.on(match_exact("sysctl", &["-n", "x"]), Output { stderr: b" unknown oid \n".to_vec(), exit_code: 1, ..Output::default() });
        let err = output(&fake, &Cmd::new("sysctl", ["-n", "x"])).expect_err("exit");
        assert_eq!(err.to_string(), "sysctl -n x exited 1: unknown oid");
    }

    #[test]
    fn fake_runner_matches_first_rule_and_records_calls() {
        let fake = FakeRunner::new();
        fake.on_stdout(match_prefix("ps", &["-o"]), "first").on_stdout(match_prefix("ps", &[]), "second");
        assert_eq!(output(&fake, &Cmd::new("ps", ["-o", "pid"])).expect("ps"), b"first");
        assert_eq!(output(&fake, &Cmd::new("ps", ["aux"])).expect("ps"), b"second");
        assert!(matches!(fake.run(&Cmd::new("top", ["-l1"])), Err(RunError::Unmatched { .. })));
        let calls: Vec<String> = fake.calls().iter().map(|c| c.name.clone()).collect();
        assert_eq!(calls, vec!["ps", "ps", "top"]);
    }
}
