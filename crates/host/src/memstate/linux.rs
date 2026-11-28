//! The Linux backend (Go's `internal/memstate/linux.go`): `/proc/meminfo`, `/proc/vmstat` and,
//! when the kernel exposes it, PSI.

use std::collections::HashMap;
use std::path::Path;

use spectra_base::gojson::GoTime;

use super::{Error, MemoryState, Options, PROC_ROOT, PressureLevel, SwapUsage};
use crate::goerr::parse_uint;
use crate::pressure::{self, gopath};

// Available memory matches hostwatch's Linux sample; the PSI cut-offs match the pressure
// assessment.
const AVAIL_WARN_PCT: f64 = 15.0;
const AVAIL_CRIT_PCT: f64 = 5.0;
const PSI_SOME_WARN_PCT: f64 = 10.0;
const PSI_FULL_WARN_PCT: f64 = 1.0;
const PSI_FULL_CRIT_PCT: f64 = 10.0;

/// Go's `collectLinux`.
pub(super) fn collect(opts: &Options<'_>) -> Result<MemoryState, Error> {
    let root = if opts.proc_root.is_empty() { PROC_ROOT } else { opts.proc_root.as_str() };
    let path = gopath::join(&[root, "meminfo"]);
    let data = opts.fs.read(Path::new(&path)).map_err(|source| Error::ReadMeminfo { path: path.clone(), source })?;
    let mut ms = parse_meminfo(&data)?;
    ms.page_size_bytes = if opts.page_size != 0 { opts.page_size } else { opts.source.page_size() };
    ms.collected_at = GoTime::utc(opts.clock.now());
    if let Ok(vm) = opts.fs.read(Path::new(&gopath::join(&[root, "vmstat"]))) {
        apply_proc_vmstat(&mut ms, &vm);
    }
    if let Ok(psi) = pressure::read_psi(opts.fs, root) {
        ms.psi = Some(psi.memory);
    }
    (ms.pressure_level, ms.pressure_reason) = linux_pressure(&ms);
    Ok(ms)
}

/// Go's `ParseMeminfo`: `/proc/meminfo` (KiB values) as a [`MemoryState`]. Unevictable
/// (mlocked and otherwise unreclaimable pages) stands in for wired memory. Zswap is the
/// compressed swap cache: `Zswap` is the compressed size held in RAM and `Zswapped` the
/// uncompressed data it stores, which are the compressor's occupied and stored sizes.
pub fn parse_meminfo(data: &[u8]) -> Result<MemoryState, Error> {
    let text = String::from_utf8_lossy(data);
    let mut kib: HashMap<&str, u64> = HashMap::new();
    for line in text.lines() {
        let Some((key, rest)) = line.split_once(':') else { continue };
        let Some(first) = rest.split_whitespace().next() else { continue };
        if let Ok(v) = parse_uint(first) {
            kib.insert(key, v);
        }
    }
    if kib.get("MemTotal").copied().unwrap_or(0) == 0 {
        return Err(Error::NoMemTotal);
    }
    let b = |k: &str| kib.get(k).copied().unwrap_or(0).wrapping_mul(1024);
    let mut ms = MemoryState {
        os: "linux".to_string(),
        physical_bytes: b("MemTotal"),
        free: b("MemFree"),
        available_bytes: b("MemAvailable"),
        active: b("Active"),
        inactive: b("Inactive"),
        wired: b("Unevictable"),
        file_backed: b("Active(file)").wrapping_add(b("Inactive(file)")),
        anonymous: b("AnonPages"),
        compressor_occupied: b("Zswap"),
        compressor_stored: b("Zswapped"),
        swap: SwapUsage { total_bytes: b("SwapTotal"), free_bytes: b("SwapFree"), ..SwapUsage::default() },
        ..MemoryState::default()
    };
    if !kib.contains_key("MemAvailable") {
        // Kernels before 3.14: free plus reclaimable page cache.
        ms.available_bytes = ms.free.wrapping_add(b("Cached")).wrapping_add(b("Buffers"));
    }
    if ms.swap.total_bytes > ms.swap.free_bytes {
        ms.swap.used_bytes = ms.swap.total_bytes - ms.swap.free_bytes;
    }
    if ms.compressor_occupied > 0 {
        ms.compressor_ratio = ms.compressor_stored as f64 / ms.compressor_occupied as f64;
    }
    ms.pressure_free_percent = ms.available_bytes as f64 / ms.physical_bytes as f64 * 100.0;
    Ok(ms)
}

/// Go's `applyProcVMStat`: the cumulative swap-in/out page counts.
fn apply_proc_vmstat(ms: &mut MemoryState, data: &[u8]) {
    let text = String::from_utf8_lossy(data);
    for line in text.lines() {
        let [name, value] = line.split_whitespace().collect::<Vec<_>>()[..] else { continue };
        let Ok(v) = parse_uint(value) else { continue };
        match name {
            "pswpin" => ms.swap_ins = v,
            "pswpout" => ms.swap_outs = v,
            _ => {}
        }
    }
}

/// Go's `linuxPressure`. Linux has no kernel pressure level like macOS's memorystatus, so this
/// combines how much memory is still available with how long tasks stalled waiting for memory.
pub(super) fn linux_pressure(ms: &MemoryState) -> (PressureLevel, String) {
    let avail = ms.pressure_free_percent;
    let full10 = ms.psi.and_then(|p| p.full).map(|f| f.avg10);
    let stalled_all = |v: f64| format!("all tasks stalled on memory {v:.1}% of the last 10s");
    let available = || format!("{avail:.1}% of memory available");
    if let Some(v) = full10.filter(|&v| v >= PSI_FULL_CRIT_PCT) {
        return (PressureLevel::Critical, stalled_all(v));
    }
    if avail < AVAIL_CRIT_PCT {
        return (PressureLevel::Critical, available());
    }
    if let Some(v) = full10.filter(|&v| v >= PSI_FULL_WARN_PCT) {
        return (PressureLevel::Warning, stalled_all(v));
    }
    if let Some(some10) = ms.psi.map(|p| p.some.avg10).filter(|&v| v >= PSI_SOME_WARN_PCT) {
        return (PressureLevel::Warning, format!("some tasks stalled on memory {some10:.1}% of the last 10s"));
    }
    if avail < AVAIL_WARN_PCT {
        return (PressureLevel::Warning, available());
    }
    (PressureLevel::Normal, available())
}
