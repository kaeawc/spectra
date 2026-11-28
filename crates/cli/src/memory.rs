//! `spectra memory` (`cmd/spectra/memory.go`): host memory pressure, compressor and swap, once
//! or every `--watch` interval. The sample comes from [`spectra_host::memstate`] for `ctx.os`:
//! the Mach readings of `ctx.memory` on macOS, `/proc` through `ctx.fs` on Linux.

use std::io::Write;
use std::time::Duration;

use spectra_base::gojson::{Encoder, GoDuration, GoTime};
use spectra_host::goerr::parse_int;
use spectra_host::memstate::{self, MemoryState};

use crate::api::{Cli, Ctx};
use crate::flags::{FlagSet, goconv};

pub use spectra_host::memstate::{PressureLevel, SwapUsage};

/// What [`crate::api::memory`] returns.
pub type Report = MemoryState;

/// What [`crate::api::memory`] reads.
#[derive(Clone, Debug, Default)]
pub struct Options {
    /// The Linux procfs root, read through `ctx.fs`; empty means `/proc`.
    pub proc_root: String,
}

/// One sample for `ctx.os`.
pub fn collect(ctx: &Ctx, opts: &Options) -> Result<Report, memstate::Error> {
    memstate::collect(&memstate::Options {
        os: ctx.os,
        fs: ctx.fs.as_ref(),
        clock: ctx.clock.as_ref(),
        source: ctx.memory.as_ref(),
        proc_root: opts.proc_root.clone(),
        page_size: 0,
    })
}

/// Go's `runMemory`.
pub fn run(cli: &mut Cli<'_>, args: &[String]) -> i32 {
    let mut fs = FlagSet::new("spectra memory");
    let as_json = fs.bool("json", false, "Emit JSON");
    let watch = fs.string("watch", "", "Repeat every interval (for example: 1, 1s, 5s)");
    if fs.parse(args, cli.stderr).is_err() {
        return 2;
    }
    let interval = match parse_watch(&watch.get()) {
        Ok(d) => d,
        Err(e) => {
            let _ = writeln!(cli.stderr, "{e}");
            return 2;
        }
    };
    if interval.0 > 0 {
        return run_watch(cli, interval, as_json.get());
    }
    let state = match crate::api::memory(cli.ctx, Options::default()) {
        Ok(s) => s,
        Err(e) => return print_err(cli, &e),
    };
    if as_json.get() {
        // Go's json.NewEncoder(os.Stdout) with SetIndent("", "  ").
        let _ = Encoder { indent: true, escape_html: true }.encode(&mut *cli.stdout, &state);
        return 0;
    }
    write_state(cli.stdout, &state, &MemoryState::default());
    0
}

fn print_err(cli: &mut Cli<'_>, e: &memstate::Error) -> i32 {
    let _ = writeln!(cli.stderr, "{e}");
    1
}

/// Go's `runMemoryWatch`: a sample every `interval` until one fails, one JSON document per
/// line, or one text line with rates since the previous sample. Ticks follow Go's
/// `time.Ticker`: a fixed schedule from the start, and a tick missed while sampling is not
/// made up.
fn run_watch(cli: &mut Cli<'_>, interval: GoDuration, as_json: bool) -> i32 {
    let (clock, sleeper) = (cli.ctx.clock.clone(), cli.ctx.sleeper.clone());
    let period = i128::from(interval.0);
    let mut next = clock.now().as_nanosecond() + period;
    let mut prev = MemoryState::default();
    loop {
        let state = match crate::api::memory(cli.ctx, Options::default()) {
            Ok(s) => s,
            Err(e) => return print_err(cli, &e),
        };
        if as_json {
            let _ = Encoder::default().encode(&mut *cli.stdout, &state);
        } else {
            write_state(cli.stdout, &state, &prev);
        }
        prev = state;
        let now = clock.now().as_nanosecond();
        if now < next {
            sleeper.sleep(Duration::from_nanos(u64::try_from(next - now).unwrap_or(u64::MAX)));
            next += period;
        } else {
            next += period * ((now - next) / period + 1);
        }
    }
}

/// Go's `parseMemoryWatch`: whole seconds (`5`) or a Go duration (`1m30s`); empty means no
/// watch.
pub fn parse_watch(value: &str) -> Result<GoDuration, String> {
    if value.is_empty() {
        return Ok(GoDuration(0));
    }
    if let Ok(seconds) = parse_int(value) {
        if seconds <= 0 {
            return Err("--watch must be positive".to_string());
        }
        // Go's time.Duration(seconds) * time.Second, which wraps on overflow.
        return Ok(GoDuration(seconds.wrapping_mul(1_000_000_000)));
    }
    let d = goconv::parse_duration(value).map_err(|e| format!("invalid --watch: {e}"))?;
    if d.0 <= 0 {
        return Err("--watch must be positive".to_string());
    }
    Ok(d)
}

/// Go's `printMemoryState`: one line of the sample, with rates since `prev` unless `prev` is
/// the zero sample; on Linux, the pressure reason on a second line for the first sample.
pub fn write_state(out: &mut dyn Write, s: &MemoryState, prev: &MemoryState) {
    if s.os == "linux" {
        write_linux_state(out, s, prev);
        return;
    }
    let mut parts = vec![
        format!("phys={}", memory_bytes(s.physical_bytes)),
        format!("wired={}", memory_bytes(s.wired)),
        format!("app={}", memory_bytes(s.anonymous)),
        format!(
            "compressed={}(stored={} ratio={:.1}x)",
            memory_bytes(s.compressor_occupied),
            memory_bytes(s.compressor_stored),
            s.compressor_ratio
        ),
        format!("swap={}", memory_bytes(s.swap.used_bytes)),
        format!("pressure={}", s.pressure_level),
        format!("free={:.0}%", s.pressure_free_percent),
        format!("pages_free={:.0}%", s.free_pages_percent),
    ];
    if !prev.collected_at.is_zero() {
        let elapsed = seconds_between(s.collected_at, prev.collected_at);
        if elapsed > 0.0 {
            parts.push(format!("compressions/s={:.0}", rate(s.compressions, prev.compressions, elapsed)));
            parts.push(format!("swapouts/s={:.0}", rate(s.swap_outs, prev.swap_outs, elapsed)));
        }
    }
    let _ = writeln!(out, "{}", parts.join("  "));
}

fn write_linux_state(out: &mut dyn Write, s: &MemoryState, prev: &MemoryState) {
    let mut parts = vec![
        format!("phys={}", memory_bytes(s.physical_bytes)),
        format!("available={}({:.0}%)", memory_bytes(s.available_bytes), s.pressure_free_percent),
        format!("anon={}", memory_bytes(s.anonymous)),
        format!("file={}", memory_bytes(s.file_backed)),
    ];
    if s.compressor_occupied > 0 {
        parts.push(format!(
            "zswap={}(stored={} ratio={:.1}x)",
            memory_bytes(s.compressor_occupied),
            memory_bytes(s.compressor_stored),
            s.compressor_ratio
        ));
    }
    parts.push(format!("swap={}/{}", memory_bytes(s.swap.used_bytes), memory_bytes(s.swap.total_bytes)));
    if let Some(psi) = &s.psi {
        let mut text = format!("psi_some10={:.1}%", psi.some.avg10);
        if let Some(full) = &psi.full {
            text.push_str(&format!(" psi_full10={:.1}%", full.avg10));
        }
        parts.push(text);
    }
    parts.push(format!("pressure={}", s.pressure_level));
    if !prev.collected_at.is_zero() {
        let elapsed = seconds_between(s.collected_at, prev.collected_at);
        if elapsed > 0.0 {
            parts.push(format!("swapins/s={:.0}", rate(s.swap_ins, prev.swap_ins, elapsed)));
            parts.push(format!("swapouts/s={:.0}", rate(s.swap_outs, prev.swap_outs, elapsed)));
        }
    }
    let _ = writeln!(out, "{}", parts.join("  "));
    if !s.pressure_reason.is_empty() && prev.collected_at.is_zero() {
        let _ = writeln!(out, "pressure: {}", s.pressure_reason);
    }
}

/// Go's `t.Sub(u).Seconds()`: the difference saturates at Go's `Duration` range, and seconds
/// are computed as Go computes them.
fn seconds_between(t: GoTime, u: GoTime) -> f64 {
    let d = t.instant.as_nanosecond() - u.instant.as_nanosecond();
    let d = i64::try_from(d).unwrap_or(if d < 0 { i64::MIN } else { i64::MAX });
    let (sec, nsec) = (d / 1_000_000_000, d % 1_000_000_000);
    sec as f64 + nsec as f64 / 1e9
}

/// Go's `rate`: per-second change of a cumulative counter, 0 when it went backwards.
pub fn rate(current: u64, previous: u64, seconds: f64) -> f64 {
    if current < previous {
        return 0.0;
    }
    (current - previous) as f64 / seconds
}

/// Go's `memoryBytes`.
pub fn memory_bytes(n: u64) -> String {
    const K: u64 = 1024;
    if n >= K * K * K {
        format!("{:.1}GB", n as f64 / (K * K * K) as f64)
    } else if n >= K * K {
        format!("{:.0}MB", n as f64 / (K * K) as f64)
    } else if n >= K {
        format!("{:.0}KB", n as f64 / K as f64)
    } else {
        format!("{n}B")
    }
}

#[cfg(test)]
mod tests;
