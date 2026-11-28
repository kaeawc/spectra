//! `spectra pressure` (`cmd/spectra/pressure.go`): Linux pressure-stall information and, for a
//! process, its cgroup v2 memory, CPU and pids accounting, with the findings drawn from them.
//! Exits 3 when any finding is worse than info. Off Linux it refuses, pointing at
//! `spectra memory`.

use std::fmt;
use std::io::Write;

use serde::Serialize;
use spectra_base::gojson::{self, Encoder};
use spectra_base::hostos::{Kind, Unsupported};
use spectra_host::pressure::{self as psi, Cgroup, Finding, Psi, Severity};

use crate::api::{Cli, Ctx};
use crate::flags::FlagSet;

/// The JSON shape of `spectra pressure` (Go's `pressureReport`).
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Report {
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub psi: Option<Psi>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub cgroup: Option<Cgroup>,
    pub findings: Vec<Finding>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub notes: Vec<String>,
}

impl Report {
    /// The command's exit code: 3 when any finding is worse than info, else 0.
    pub fn exit_code(&self) -> i32 {
        if self.findings.iter().any(|f| f.severity != Severity::Info) { 3 } else { 0 }
    }
}

/// What [`crate::api::pressure`] reads, through `ctx.fs`.
#[derive(Clone, Debug)]
pub struct Options {
    /// Also read this process's cgroup v2 accounting when positive.
    pub pid: i64,
    pub proc_root: String,
    pub cgroup_root: String,
}

impl Default for Options {
    fn default() -> Self {
        Options { pid: 0, proc_root: "/proc".to_string(), cgroup_root: "/sys/fs/cgroup".to_string() }
    }
}

/// Why there is no report.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Error {
    /// Not Linux.
    Unsupported(Unsupported),
    /// A PSI or cgroup file could not be read or parsed (not merely absent).
    Read(psi::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Unsupported(u) => u.fmt(f),
            Error::Read(e) => e.fmt(f),
        }
    }
}

impl std::error::Error for Error {}

/// The feature named when the command runs off Linux.
const FEATURE: &str = "pressure-stall information";
const PSI_UNAVAILABLE: &str = "PSI is not available on this kernel (needs Linux 4.20+ with CONFIG_PSI, and not booted with psi=0)";

/// Go's `runPressureWithIO` after flag parsing.
pub fn collect(ctx: &Ctx, opts: &Options) -> Result<Report, Error> {
    let os = ctx.os.resolve();
    if os != Kind::Linux {
        return Err(Error::Unsupported(Unsupported::new(FEATURE, os)));
    }
    let fs = ctx.fs.as_ref();
    let mut rep = Report::default();
    match psi::read_psi(fs, &opts.proc_root) {
        Ok(p) => {
            rep.findings.extend(psi::assess_psi(&p));
            rep.psi = Some(p);
        }
        Err(e) if e.is_unavailable() => rep.notes.push(PSI_UNAVAILABLE.to_string()),
        Err(e) => return Err(Error::Read(e)),
    }
    if opts.pid > 0 {
        match psi::read_cgroup(fs, &opts.proc_root, &opts.cgroup_root, opts.pid) {
            Ok(cg) => {
                rep.findings.extend(psi::assess_cgroup(&cg));
                rep.cgroup = Some(cg);
            }
            Err(e) if e.is_unavailable() => {
                rep.notes.push(format!("pid {} is not on a cgroup v2 hierarchy; cgroup accounting skipped", opts.pid));
            }
            Err(e) => return Err(Error::Read(e)),
        }
    }
    Ok(rep)
}

/// Go's `runPressure`.
pub fn run(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let mut fs = FlagSet::new("spectra pressure");
    let as_json = fs.bool("json", false, "Emit JSON instead of a human summary");
    let pid = fs.int("pid", 0, "Also read the cgroup v2 accounting for this process");
    if fs.parse(args, cli.stderr).is_err() {
        return 2;
    }
    let rep = match crate::api::pressure(cli.ctx, Options { pid: pid.get(), ..Options::default() }) {
        Ok(r) => r,
        Err(Error::Unsupported(u)) => {
            let _ = writeln!(cli.stderr, "spectra pressure: {u} (use `spectra memory` on macOS)");
            return 1;
        }
        Err(Error::Read(e)) => {
            let _ = writeln!(cli.stderr, "spectra pressure: {e}");
            return 1;
        }
    };
    if as_json.get() {
        // Go's json.NewEncoder(stdout) with SetIndent("", "  ").
        let _ = Encoder { indent: true, escape_html: true }.encode(&mut *cli.stdout, &rep);
    } else {
        write_text(cli.stdout, &rep);
    }
    rep.exit_code()
}

/// Go's `printPressure`.
pub fn write_text(w: &mut dyn Write, r: &Report) {
    if let Some(p) = &r.psi {
        let _ = writeln!(w, "=== pressure stall (share of the last 10s / 60s / 300s tasks were stalled) ===");
        for (name, res) in [("cpu", &p.cpu), ("memory", &p.memory), ("io", &p.io)] {
            let _ = write!(w, "  {name:<7} some {:5.1}% {:5.1}% {:5.1}%", res.some.avg10, res.some.avg60, res.some.avg300);
            if let Some(full) = res.full.filter(|_| name != "cpu") {
                let _ = write!(w, "   full {:5.1}% {:5.1}% {:5.1}%", full.avg10, full.avg60, full.avg300);
            }
            let _ = writeln!(w);
        }
    }
    if let Some(c) = &r.cgroup {
        write_cgroup(w, c);
    }
    if r.findings.is_empty() {
        let _ = writeln!(w, "no pressure problems found");
    }
    for f in &r.findings {
        let _ = writeln!(w, "[{}] {}: {}", f.severity.as_str(), f.kind, f.summary);
    }
    for n in &r.notes {
        let _ = writeln!(w, "note: {n}");
    }
}

fn write_cgroup(w: &mut dyn Write, c: &Cgroup) {
    let _ = writeln!(w, "=== cgroup {} ===", c.path);
    if let Some(m) = &c.memory {
        let mut limit = "unlimited".to_string();
        if m.effective_max > 0 {
            limit = format!("{} MiB", m.effective_max >> 20);
            if !m.limited_by.is_empty() && m.limited_by != c.path {
                limit.push_str(&format!(" (from {})", m.limited_by));
            }
        }
        let _ = writeln!(
            w,
            "  memory: {} MiB used (anon {} MiB, file {} MiB), limit {limit}, swap {} MiB",
            m.current >> 20,
            m.anon >> 20,
            m.file >> 20,
            m.swap_current >> 20
        );
        let e = &m.events;
        let _ = writeln!(w, "  events: max={} high={} oom={} oom_kill={}", e.max, e.high, e.oom, e.oom_kill);
    }
    if let Some(cpu) = &c.cpu {
        let _ = writeln!(
            w,
            "  cpu:    {:.1}s used, throttled {}/{} periods ({:.1}s)",
            cpu.usage_usec as f64 / 1e6,
            cpu.nr_throttled,
            cpu.nr_periods,
            cpu.throttled_usec as f64 / 1e6
        );
    }
    if let Some(p) = &c.pids {
        let _ = writeln!(w, "  pids:   {} / {}", p.current, p.max);
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use super::*;
    use spectra_base::env::MapEnv;

    fn run_on(os: Kind, args: &[&str]) -> (i32, String, String) {
        let ctx =
            Ctx { os, env: Arc::new(MapEnv::default()), fs: Arc::new(spectra_base::fs::RootedFs::new("/nonexistent")), ..Ctx::system("v") };
        let args: Vec<String> = args.iter().map(|s| s.to_string()).collect();
        let (mut out, mut err) = (Vec::new(), Vec::new());
        let code = crate::run(&args, &ctx, &mut out, &mut err);
        (code, String::from_utf8(out).expect("utf8"), String::from_utf8(err).expect("utf8"))
    }

    #[test]
    fn refuses_off_linux_after_parsing_flags() {
        let (code, out, err) = run_on(Kind::Darwin, &["pressure", "--json", "--pid", "1"]);
        assert_eq!((code, out.as_str()), (1, ""));
        assert_eq!(err, "spectra pressure: pressure-stall information: unsupported on host OS (darwin) (use `spectra memory` on macOS)\n");
        assert_eq!(run_on(Kind::Darwin, &["pressure", "--nope"]).0, 2, "flags are parsed first");
    }

    #[test]
    fn linux_without_psi_notes_it() {
        let (code, out, err) = run_on(Kind::Linux, &["pressure", "--json"]);
        assert_eq!((code, err.as_str()), (0, ""));
        assert!(out.starts_with("{\n  \"findings\": [],\n  \"notes\": [\n    \"PSI is not available"), "{out}");
    }

    #[test]
    fn exit_code_ignores_info_findings() {
        let f = |severity| Finding { kind: "k".into(), severity, summary: String::new() };
        assert_eq!(Report { findings: vec![f(Severity::Info)], ..Report::default() }.exit_code(), 0);
        assert_eq!(Report { findings: vec![f(Severity::Info), f(Severity::Warning)], ..Report::default() }.exit_code(), 3);
        assert_eq!(Report::default().exit_code(), 0);
    }
}
