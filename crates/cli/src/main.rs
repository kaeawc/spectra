//! The `spectra` binary: [`spectra_cli::run`] over the real host.

use std::io::Write;
use std::process::ExitCode;

/// Release builds stamp the version at compile time (`SPECTRA_VERSION=vX.Y.Z cargo build`), as
/// Go's release builds do with `-ldflags -X main.version`; source builds report "dev".
const VERSION: &str = match option_env!("SPECTRA_VERSION") {
    Some(v) => v,
    None => spectra_cli::api::DEV_VERSION,
};

fn main() -> ExitCode {
    // Go's arguments are byte strings; arguments that aren't UTF-8 are read lossily here.
    let args: Vec<String> = std::env::args_os().skip(1).map(|a| a.to_string_lossy().into_owned()).collect();
    let ctx = spectra_cli::Ctx::system(VERSION);
    let (stdout, stderr) = (std::io::stdout(), std::io::stderr());
    let (mut out, mut err) = (stdout.lock(), stderr.lock());
    let code = spectra_cli::run(&args, &ctx, &mut out, &mut err);
    let _ = out.flush();
    // Exit codes outside 0..=255 wrap, as they do for Go's os.Exit.
    ExitCode::from(code as u8)
}
