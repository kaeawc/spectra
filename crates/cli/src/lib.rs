//! Spectra's command layer: the `spectra` binary's dispatch, Go `flag`-compatible parsing, help
//! text and the macOS-only gate (`cmd/spectra` in the Go build). The binary is a thin `main`
//! over [`run`]; the desktop and tests call [`run`] or the [`api`] functions with their own
//! [`Ctx`].

pub mod api;
pub mod capabilities;
pub mod dispatch;
pub mod flags;
pub mod help;
pub mod lineage;
pub mod memory;
pub mod pressure;
pub mod procdiag;
pub mod process;
pub mod system;
pub mod unsupported;

use std::io::Write;

pub use api::Ctx;

/// Runs `spectra <args>` (without the program name) against `ctx`, writing to `stdout` and
/// `stderr`, and returns the exit code (Go's `dispatch`).
pub fn run(args: &[String], ctx: &Ctx, stdout: &mut dyn Write, stderr: &mut dyn Write) -> i32 {
    let (args, verbose) = dispatch::strip_verbose_flags(args, ctx.env.as_ref());
    let mut cli = api::Cli::new(ctx, verbose, stdout, stderr);
    dispatch::dispatch(&mut cli, &args)
}
