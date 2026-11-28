//! The whole-machine memory-pressure and swap signals macOS (`sysctl`) and Linux (`/proc`,
//! through [`memstate`]) expose without root (Go's `internal/sysload`). It complements the
//! per-process and thermal collectors, so one "why is this machine slow?" triage can rank
//! causes.

use serde::Serialize;
use spectra_base::hostos::Kind;
use spectra_base::proc::{self, Cmd, Runner};

use crate::goerr::{parse_float, parse_int};
use crate::memstate::{self, MemoryState, PressureLevel};

/// The machine's current memory-pressure and swap state.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
pub struct Load {
    /// `normal`, `warn`, `critical`, `unknown`, or `level-<n>` for any other level.
    pub memory_pressure: String,
    pub pressure_level: i64,
    pub swap_used_mb: f64,
    pub swap_total_mb: f64,
}

/// Go's `Collect`: on Linux from a [`memstate`] sample, elsewhere from `sysctl` through
/// `runner`.
pub fn collect(runner: &dyn Runner, mem: &memstate::Options<'_>) -> Load {
    if mem.os.resolve() == Kind::Linux {
        return from_mem_state(memstate::collect(mem));
    }
    let mut l = Load { memory_pressure: "unknown".to_string(), ..Load::default() };
    let sysctl = |name: &str| proc::output(runner, &Cmd::new("sysctl", ["-n", name]).bounded()).ok();
    if let Some(out) = sysctl("kern.memorystatus_vm_pressure_level")
        && let Ok(lvl) = parse_int(String::from_utf8_lossy(&out).trim())
    {
        l.pressure_level = lvl;
        l.memory_pressure = pressure_label(lvl);
    }
    if let Some(out) = sysctl("vm.swapusage") {
        let s = String::from_utf8_lossy(&out);
        l.swap_used_mb = parse_swap_field("used", &s);
        l.swap_total_mb = parse_swap_field("total", &s);
    }
    l
}

/// Go's `fromMemState`: a [`memstate`] sample as a [`Load`], with the macOS memorystatus level
/// numbers (1 normal, 2 warn, 4 critical).
pub fn from_mem_state(sample: Result<MemoryState, memstate::Error>) -> Load {
    let mut l = Load { memory_pressure: "unknown".to_string(), ..Load::default() };
    let Ok(ms) = sample else { return l };
    l.pressure_level = match ms.pressure_level {
        PressureLevel::Normal => 1,
        PressureLevel::Warning => 2,
        PressureLevel::Critical => 4,
        _ => 0,
    };
    if l.pressure_level != 0 {
        l.memory_pressure = pressure_label(l.pressure_level);
    }
    l.swap_used_mb = ms.swap.used_bytes as f64 / (1u64 << 20) as f64;
    l.swap_total_mb = ms.swap.total_bytes as f64 / (1u64 << 20) as f64;
    l
}

fn pressure_label(lvl: i64) -> String {
    match lvl {
        1 => "normal".to_string(),
        2 => "warn".to_string(),
        4 => "critical".to_string(),
        _ => format!("level-{lvl}"),
    }
}

/// RE2's `\s`.
fn is_re2_space(b: u8) -> bool {
    matches!(b, b' ' | b'\t' | b'\n' | b'\x0c' | b'\r')
}

/// Go's `parseSwapField` with its `<field>\s*=\s*([0-9.]+)([MG]?)` regexp: a megabyte value out
/// of a `vm.swapusage` string like `total = 2048.00M  used = 669.88M  free = 1378.12M`.
fn parse_swap_field(field: &str, s: &str) -> f64 {
    let b = s.as_bytes();
    let matched = s.match_indices(field).find_map(|(at, _)| {
        let mut i = at + field.len();
        while i < b.len() && is_re2_space(b[i]) {
            i += 1;
        }
        if b.get(i) != Some(&b'=') {
            return None;
        }
        i += 1;
        while i < b.len() && is_re2_space(b[i]) {
            i += 1;
        }
        let start = i;
        while i < b.len() && (b[i].is_ascii_digit() || b[i] == b'.') {
            i += 1;
        }
        (i > start).then(|| (&s[start..i], b.get(i).copied()))
    });
    let Some((number, unit)) = matched else { return 0.0 };
    let Ok(v) = parse_float(number) else { return 0.0 };
    if unit == Some(b'G') { v * 1024.0 } else { v }
}

#[cfg(test)]
mod tests {
    use super::*;
    use spectra_base::clock::FakeClock;
    use spectra_base::proc::{FakeRunner, Output, match_exact};

    use crate::memstate::{FixedMemory, Options};
    use crate::testfs::MapFs;

    fn darwin_load(pressure: &str, swap: &str) -> Load {
        let runner = FakeRunner::new();
        let out = |s: &str| Output { stdout: s.as_bytes().to_vec(), stderr: Vec::new(), exit_code: 0 };
        runner.on(match_exact("sysctl", &["-n", "kern.memorystatus_vm_pressure_level"]), out(pressure));
        runner.on(match_exact("sysctl", &["-n", "vm.swapusage"]), out(swap));
        let (fs, clock, source) = (MapFs::default(), FakeClock::new(jiff::Timestamp::UNIX_EPOCH), FixedMemory::default());
        collect(&runner, &Options { os: Kind::Darwin, fs: &fs, clock: &clock, source: &source, proc_root: String::new(), page_size: 0 })
    }

    // Go: TestCollect.
    #[test]
    fn collects_darwin_from_sysctl() {
        let l = darwin_load("4\n", "total = 2048.00M  used = 669.88M  free = 1378.12M  (encrypted)");
        assert_eq!((l.memory_pressure.as_str(), l.pressure_level), ("critical", 4));
        assert_eq!((l.swap_used_mb, l.swap_total_mb), (669.88, 2048.0));
    }

    // Go: TestPressureLabels.
    #[test]
    fn pressure_labels() {
        for (input, want) in [("1", "normal"), ("2", "warn"), ("4", "critical"), ("3", "level-3")] {
            assert_eq!(darwin_load(input, "").memory_pressure, want, "{input}");
        }
    }

    // Go: TestSwapGigabytes.
    #[test]
    fn swap_in_gigabytes() {
        assert_eq!(darwin_load("1", "total = 4.00G  used = 2.50G  free = 1.50G").swap_used_mb, 2560.0);
    }

    // Go: TestMalformedGraceful.
    #[test]
    fn malformed_output_is_graceful() {
        let l = darwin_load("not-a-number", "garbage");
        assert_eq!((l.memory_pressure.as_str(), l.swap_used_mb), ("unknown", 0.0));
        assert_eq!(parse_swap_field("used", "used=1.2.3M"), 0.0, "ParseFloat rejects it");
        assert_eq!(parse_swap_field("used", "unused x used\t=\t7"), 7.0, "the first occurrence that matches");
    }

    // Go: TestFromMemState.
    #[test]
    fn from_a_mem_state() {
        let mut ms = MemoryState { pressure_level: PressureLevel::Warning, ..MemoryState::default() };
        (ms.swap.used_bytes, ms.swap.total_bytes) = (512 << 20, 2048 << 20);
        let l = from_mem_state(Ok(ms));
        assert_eq!(l, Load { memory_pressure: "warn".into(), pressure_level: 2, swap_used_mb: 512.0, swap_total_mb: 2048.0 });
        assert_eq!(from_mem_state(Err(memstate::Error::NoMemTotal)).memory_pressure, "unknown");
        let unknown = MemoryState { pressure_level: PressureLevel::Unknown, ..MemoryState::default() };
        assert_eq!(from_mem_state(Ok(unknown)).memory_pressure, "unknown");
    }

    #[test]
    fn linux_reads_memstate() {
        let fs = MapFs::new([("/proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 50 kB\nSwapTotal: 2048 kB\nSwapFree: 1024 kB\n")]);
        let (clock, source) = (FakeClock::new(jiff::Timestamp::UNIX_EPOCH), FixedMemory::default());
        let opts = Options { os: Kind::Linux, fs: &fs, clock: &clock, source: &source, proc_root: String::new(), page_size: 4096 };
        let l = collect(&FakeRunner::new(), &opts);
        assert_eq!(l, Load { memory_pressure: "warn".into(), pressure_level: 2, swap_used_mb: 1.0, swap_total_mb: 2.0 });
    }
}
