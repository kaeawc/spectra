//! `spectra help` (Go's `runHelp` in `cmd/spectra/main.go`).

use std::io::Write;

use crate::dispatch::SUBCOMMANDS;
use crate::unsupported::darwin_only;

/// Writes the help text. Output errors are ignored, as Go's `fmt.Fprintln` calls are.
pub fn write(w: &mut dyn Write) {
    let _ = write_text(w);
}

fn write_text(w: &mut dyn Write) -> std::io::Result<()> {
    writeln!(w, "Spectra — macOS app diagnostics and JVM-aware debugging.")?;
    writeln!(w)?;
    writeln!(w, "Usage: spectra <subcommand> [flags] [args]")?;
    writeln!(w)?;
    writeln!(w, "Global flags:")?;
    writeln!(w, "  --verbose, --debug   Log enhancement/collection failures to stderr (or set SPECTRA_DEBUG)")?;
    writeln!(w, "  --no-daemon         Do not use the daemon for alerts, whatswrong, load and idle")?;
    writeln!(w)?;
    writeln!(w, "Subcommands:")?;
    for sc in SUBCOMMANDS {
        let mac_only = if darwin_only(sc.name).is_some() { " [macOS only]" } else { "" };
        // Go's "%-10s": pad to ten characters, never truncate.
        writeln!(w, "  {:<10} {}{mac_only}", sc.name, sc.desc)?;
    }
    writeln!(w)?;
    writeln!(w, "Flag-only invocations route to `inspect`:")?;
    writeln!(w, "  spectra /Applications/Slack.app")?;
    writeln!(w, "  spectra --all -v")?;
    writeln!(w, "  spectra list -v")?;
    writeln!(w, "  spectra --json /Applications/Cursor.app")
}
