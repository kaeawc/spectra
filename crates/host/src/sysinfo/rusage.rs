//! Per-pid energy accounting from two `proc_pid_rusage` snapshots (Go's `rusage.go`,
//! `rusage_darwin.go` and `rusage_stub.go`).

use std::collections::BTreeMap;
use std::fmt;
use std::time::Duration;

use serde::Serialize;
use spectra_base::clock::Sleeper;
use spectra_base::gojson::{self, GoDuration};
use spectra_base::proc::CancelToken;

/// The subset of `proc_pid_rusage` surfaced for per-pid energy accounting. Energy is in
/// nanojoules as the XNU scheduler (CLPC) reports it; times are in nanoseconds.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize)]
pub struct ProcRusage {
    pub pid: i64,
    pub billed_energy_nj: u64,
    pub serviced_energy_nj: u64,
    /// V6 only: `ri_energy_nj`, the total of E and P cores.
    #[serde(rename = "energy_nj", skip_serializing_if = "gojson::is_empty")]
    pub energy_nj: u64,
    /// V6 only: `ri_penergy_nj`, P cores only.
    #[serde(rename = "penergy_nj", skip_serializing_if = "gojson::is_empty")]
    pub p_energy_nj: u64,
    pub interrupt_wakeups: u64,
    pub pkg_idle_wakeups: u64,
    pub user_ns: u64,
    pub system_ns: u64,
    pub disk_bytes_read: u64,
    pub disk_bytes_written: u64,
}

/// Why a rusage read failed.
#[derive(Debug)]
pub enum RusageError {
    /// Go's `ErrRusageUnsupported`: not macOS (or no cgo in Go). Build-wide, so every pid would
    /// fail.
    Unsupported,
    /// Go's `ErrRusagePermission`: the pid is not the caller's and the entitlement is missing.
    /// Callers skip the pid and continue.
    Permission {
        pid: i64,
    },
    Other(String),
}

impl fmt::Display for RusageError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            RusageError::Unsupported => f.write_str("proc_pid_rusage unsupported on this platform"),
            RusageError::Permission { pid } => write!(f, "pid {pid}: proc_pid_rusage: permission denied"),
            RusageError::Other(m) => f.write_str(m),
        }
    }
}

impl std::error::Error for RusageError {}

/// Where rusage snapshots come from (Go's `RusageReader`). The real one is [`HostRusage`].
pub trait RusageSource: Send + Sync {
    fn read(&self, pid: i64) -> Result<ProcRusage, RusageError>;
}

/// The live host: `proc_pid_rusage` through `spectra_sys` on macOS (V6, then V4 on older
/// kernels), [`RusageError::Unsupported`] elsewhere.
#[derive(Clone, Copy, Debug, Default)]
pub struct HostRusage;

impl RusageSource for HostRusage {
    #[cfg(target_os = "macos")]
    fn read(&self, pid: i64) -> Result<ProcRusage, RusageError> {
        use spectra_sys::darwin::{self, RusageError as Sys};
        let raw = i32::try_from(pid).map_err(|_| RusageError::Other(format!("proc_pid_rusage({pid}): pid out of range")))?;
        match darwin::proc_pid_rusage(raw) {
            Ok(r) => Ok(ProcRusage {
                pid,
                billed_energy_nj: r.billed_energy_nj,
                serviced_energy_nj: r.serviced_energy_nj,
                energy_nj: r.energy_nj,
                p_energy_nj: r.p_energy_nj,
                interrupt_wakeups: r.interrupt_wakeups,
                pkg_idle_wakeups: r.pkg_idle_wakeups,
                user_ns: r.user_ns,
                system_ns: r.system_ns,
                disk_bytes_read: r.disk_bytes_read,
                disk_bytes_written: r.disk_bytes_written,
            }),
            Err(Sys::Permission) => Err(RusageError::Permission { pid }),
            Err(Sys::Other(e)) => Err(RusageError::Other(format!("proc_pid_rusage({pid}): {e}"))),
        }
    }

    #[cfg(not(target_os = "macos"))]
    fn read(&self, _pid: i64) -> Result<ProcRusage, RusageError> {
        Err(RusageError::Unsupported)
    }
}

/// The per-pid difference between two snapshots taken `interval` apart; every counter is
/// after minus before (Go's `EnergyDelta`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct EnergyDelta {
    pub pid: i64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub command: String,
    #[serde(rename = "interval_ns")]
    pub interval: GoDuration,
    pub billed_energy_nj: u64,
    pub serviced_energy_nj: u64,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub energy_nj: u64,
    #[serde(rename = "penergy_nj", skip_serializing_if = "gojson::is_empty")]
    pub p_energy_nj: u64,
    pub interrupt_wakeups: u64,
    pub pkg_idle_wakeups: u64,
    pub user_ns: u64,
    pub system_ns: u64,
    pub disk_bytes_read: u64,
    pub disk_bytes_written: u64,
}

/// Why a sample failed.
#[derive(Debug, PartialEq, Eq)]
pub enum SampleError {
    /// [`RusageError::Unsupported`], reported at once rather than as an empty sample.
    Unsupported,
    /// Go's `context.Canceled`.
    Canceled,
}

impl fmt::Display for SampleError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            SampleError::Unsupported => RusageError::Unsupported.fmt(f),
            SampleError::Canceled => f.write_str("context canceled"),
        }
    }
}

impl std::error::Error for SampleError {}

/// Diffs two rusage snapshots taken `interval` apart (Go's `EnergySampler`).
pub struct EnergySampler<'a> {
    pub interval: Duration,
    pub reader: &'a dyn RusageSource,
    /// Waits out the interval between the two snapshots.
    pub sleeper: &'a dyn Sleeper,
    /// Go's `ctx`: checked before the wait, which is not interrupted once begun.
    pub cancel: Option<CancelToken>,
}

impl EnergySampler<'_> {
    /// Go's `Sample`: before and after snapshots of `pids` and the per-pid deltas. Pids that
    /// fail with a permission error are dropped silently, as are pids gone by the second
    /// pass. Deltas come in ascending pid order (Go's map order is random).
    pub fn sample(&self, pids: &[i64]) -> Result<Vec<EnergyDelta>, SampleError> {
        let before = read_all(self.reader, pids)?;
        self.wait()?;
        let after = read_all(self.reader, pids)?;
        let interval = GoDuration::from_std(self.interval);
        Ok(before.iter().filter_map(|(pid, b)| after.get(pid).map(|a| diff_one(*pid, b, a, interval))).collect())
    }

    /// Go's `defaultSleep`: a non-positive wait is none at all; otherwise a cancelled sampler
    /// stops here.
    fn wait(&self) -> Result<(), SampleError> {
        if self.interval.is_zero() {
            return Ok(());
        }
        if self.cancel.as_ref().is_some_and(CancelToken::is_cancelled) {
            return Err(SampleError::Canceled);
        }
        self.sleeper.sleep(self.interval);
        Ok(())
    }
}

/// Skips per-pid failures (permission, exited) but returns `Unsupported` at once: it is
/// build-wide, so every pid would fail and the caller would otherwise see an empty, successful
/// sample.
fn read_all(reader: &dyn RusageSource, pids: &[i64]) -> Result<BTreeMap<i64, ProcRusage>, SampleError> {
    let mut out = BTreeMap::new();
    for &pid in pids {
        match reader.read(pid) {
            Ok(r) => {
                out.insert(pid, r);
            }
            Err(RusageError::Unsupported) => return Err(SampleError::Unsupported),
            Err(_) => {}
        }
    }
    Ok(out)
}

/// `a - b`, clamped to zero. Counters can decrease across rusage versions or when a pid is
/// recycled; clamping keeps the output honest.
fn sub(a: u64, b: u64) -> u64 {
    a.saturating_sub(b)
}

fn diff_one(pid: i64, before: &ProcRusage, after: &ProcRusage, interval: GoDuration) -> EnergyDelta {
    EnergyDelta {
        pid,
        command: String::new(),
        interval,
        billed_energy_nj: sub(after.billed_energy_nj, before.billed_energy_nj),
        serviced_energy_nj: sub(after.serviced_energy_nj, before.serviced_energy_nj),
        energy_nj: sub(after.energy_nj, before.energy_nj),
        p_energy_nj: sub(after.p_energy_nj, before.p_energy_nj),
        interrupt_wakeups: sub(after.interrupt_wakeups, before.interrupt_wakeups),
        pkg_idle_wakeups: sub(after.pkg_idle_wakeups, before.pkg_idle_wakeups),
        user_ns: sub(after.user_ns, before.user_ns),
        system_ns: sub(after.system_ns, before.system_ns),
        disk_bytes_read: sub(after.disk_bytes_read, before.disk_bytes_read),
        disk_bytes_written: sub(after.disk_bytes_written, before.disk_bytes_written),
    }
}

/// Go's `RankedEnergy`: deltas by billed energy descending, cut to `top` (0 keeps all). Ties
/// break on interrupt wakeups, then disk bytes read plus written, then pid.
pub fn ranked_energy(deltas: &[EnergyDelta], top: usize) -> Vec<EnergyDelta> {
    let mut out: Vec<EnergyDelta> = deltas.to_vec();
    out.sort_by(|a, b| {
        b.billed_energy_nj
            .cmp(&a.billed_energy_nj)
            .then(b.interrupt_wakeups.cmp(&a.interrupt_wakeups))
            .then((b.disk_bytes_read.wrapping_add(b.disk_bytes_written)).cmp(&a.disk_bytes_read.wrapping_add(a.disk_bytes_written)))
            .then(a.pid.cmp(&b.pid))
    });
    if top > 0 && out.len() > top {
        out.truncate(top);
    }
    out
}

#[cfg(test)]
mod tests;
