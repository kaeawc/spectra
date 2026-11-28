//! Go's collector `CmdRunner` (`proc.BoundedOutput`) over a [`Runner`]: stdout, and an error for
//! a failed start or a non-zero exit, keeping what a failing command printed.

use spectra_base::proc::{Cmd, Runner};

/// Runs `name args` bounded by [`spectra_base::proc::DEFAULT_TIMEOUT`]. The error is `None` when
/// it exited 0.
pub(crate) fn bounded(runner: &dyn Runner, name: &str, args: &[&str]) -> (Vec<u8>, Option<String>) {
    match runner.run(&Cmd::new(name, args.iter().copied()).bounded()) {
        Ok(out) if out.exit_code == 0 => (out.stdout, None),
        Ok(out) => {
            let code = out.exit_code;
            (out.stdout, Some(format!("exit status {code}")))
        }
        Err(e) => (Vec::new(), Some(e.to_string())),
    }
}

/// [`bounded`] when the command succeeded; `None` otherwise.
pub(crate) fn succeeded(runner: &dyn Runner, name: &str, args: &[&str]) -> Option<Vec<u8>> {
    match bounded(runner, name, args) {
        (out, None) => Some(out),
        _ => None,
    }
}
