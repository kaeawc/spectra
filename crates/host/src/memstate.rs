//! Host-wide memory pressure, compressor and swap state (Go's `internal/memstate`): from the
//! Mach VM statistics and sysctls on macOS, through a [`MemorySource`], and from `/proc`
//! (`meminfo`, `vmstat` and PSI) on Linux, through [`Fs`]. The OS comes from
//! [`Options::os`], so both paths run on any host.

mod linux;
mod vmstat;

use std::fmt;
use std::io;

use serde::{Deserialize, Deserializer, Serialize, Serializer};
use spectra_base::clock::Clock;
use spectra_base::fs::Fs;
use spectra_base::gojson::{self, GoTime};
use spectra_base::hostos::Kind;

use crate::goerr;
use crate::pressure::Resource;
pub use linux::parse_meminfo;
pub use vmstat::{parse_vm_stat, parse_xsw_usage};

/// The kernel memorystatus pressure classification. [`PressureLevel::Unset`] is Go's zero
/// value (`""`), which only an empty [`MemoryState`] carries.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum PressureLevel {
    #[default]
    Unset,
    Unknown,
    Normal,
    Warning,
    Critical,
}

impl PressureLevel {
    pub fn as_str(self) -> &'static str {
        match self {
            PressureLevel::Unset => "",
            PressureLevel::Unknown => "Unknown",
            PressureLevel::Normal => "Normal",
            PressureLevel::Warning => "Warning",
            PressureLevel::Critical => "Critical",
        }
    }

    /// The level [`PressureLevel::as_str`] names. A name this build doesn't know (a newer
    /// Spectra's) is [`PressureLevel::Unknown`], so one reading's level never fails the sample.
    pub fn from_name(name: &str) -> PressureLevel {
        match name {
            "" => PressureLevel::Unset,
            "Normal" => PressureLevel::Normal,
            "Warning" => PressureLevel::Warning,
            "Critical" => PressureLevel::Critical,
            _ => PressureLevel::Unknown,
        }
    }

    /// Go's `pressureLevel`: `kern.memorystatus_vm_pressure_level` as a level.
    pub fn from_raw(raw: u64) -> PressureLevel {
        match raw {
            1 => PressureLevel::Normal,
            2 => PressureLevel::Warning,
            4 => PressureLevel::Critical,
            _ => PressureLevel::Unknown,
        }
    }
}

impl fmt::Display for PressureLevel {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl Serialize for PressureLevel {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for PressureLevel {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let name = std::borrow::Cow::<str>::deserialize(deserializer)?;
        Ok(PressureLevel::from_name(&name))
    }
}

/// The host swap device state. A missing field reads as zero, as in [`MemoryState`].
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SwapUsage {
    pub total_bytes: u64,
    pub used_bytes: u64,
    pub free_bytes: u64,
    pub encrypted: bool,
}

/// One point-in-time host memory sample (Go's `MemoryState`); field order and `omitempty`
/// match its JSON. It reads back from that JSON (the Go daemon's `memory.current` answers in
/// it), with a missing field left at its zero value, as Go's `json.Unmarshal` leaves it.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct MemoryState {
    pub physical_bytes: u64,
    pub page_size_bytes: u64,
    pub wired: u64,
    pub active: u64,
    pub inactive: u64,
    pub speculative: u64,
    pub free: u64,
    pub purgeable: u64,
    pub file_backed: u64,
    pub anonymous: u64,
    pub compressor_occupied: u64,
    pub compressor_stored: u64,
    pub compressor_ratio: f64,
    pub swap: SwapUsage,
    pub pressure_level: PressureLevel,
    /// On macOS, `kern.memorystatus_level` (what `memory_pressure` reports as the system-wide
    /// free percentage) when it reads as at most 100, else [`MemoryState::free_pages_percent`];
    /// on Linux, MemAvailable as a share of MemTotal.
    pub pressure_free_percent: f64,
    /// (Free+Speculative)/Physical on macOS, unset on Linux. Not a pressure headroom figure: the
    /// kernel keeps few pages literally free.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub free_pages_percent: f64,
    pub compressions: u64,
    pub decompressions: u64,
    pub swap_ins: u64,
    pub swap_outs: u64,
    pub collected_at: GoTime,
    /// Linux only: the backend that filled the sample.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub os: String,
    /// Linux only: MemAvailable, what can be allocated without swapping.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub available_bytes: u64,
    /// Linux only: the kernel's memory pressure-stall reading, absent when it has none.
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub psi: Option<Resource>,
    /// Linux only: what decided [`MemoryState::pressure_level`].
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub pressure_reason: String,
}

/// The `host_statistics64(HOST_VM_INFO64)` counters the macOS collector reads: page counts,
/// except the four cumulative event counters at the end (Go's `DarwinVMCounts`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct DarwinVmCounts {
    #[serde(rename = "wire_count")]
    pub wire: u64,
    #[serde(rename = "active_count")]
    pub active: u64,
    #[serde(rename = "inactive_count")]
    pub inactive: u64,
    #[serde(rename = "speculative_count")]
    pub speculative: u64,
    #[serde(rename = "free_count")]
    pub free: u64,
    #[serde(rename = "purgeable_count")]
    pub purgeable: u64,
    #[serde(rename = "external_page_count")]
    pub external: u64,
    #[serde(rename = "internal_page_count")]
    pub internal: u64,
    #[serde(rename = "compressor_page_count")]
    pub compressor: u64,
    #[serde(rename = "total_uncompressed_pages_in_compressor")]
    pub uncompressed_compressor: u64,
    pub compressions: u64,
    pub decompressions: u64,
    pub swapins: u64,
    pub swapouts: u64,
}

/// One raw macOS reading (Go's `DarwinSample`): what [`from_darwin_sample`] assembles. A `None`
/// level means that sysctl could not be read. Recorded contract cases hold one in this shape.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct DarwinSample {
    pub page_size_bytes: u64,
    /// `hw.memsize`, 0 when it could not be read.
    pub physical_bytes: u64,
    pub swap: SwapUsage,
    pub vm: DarwinVmCounts,
    /// `kern.memorystatus_vm_pressure_level`.
    pub pressure_level: Option<u32>,
    /// `kern.memorystatus_level`.
    pub memorystatus_level: Option<u32>,
}

/// Why a sample could not be taken. The texts are the Go build's.
#[derive(Debug)]
pub enum Error {
    /// Neither the macOS backend nor Linux (Go's `ErrNotSupported`).
    NotSupported,
    /// `<proc>/meminfo` could not be read.
    ReadMeminfo { path: String, source: io::Error },
    /// `meminfo` has no `MemTotal`.
    NoMemTotal,
    /// `sysctl vm.swapusage` failed.
    SwapUsage(io::Error),
    /// `host_statistics64` failed with this `kern_return_t`.
    VmStatistics(i32),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::NotSupported => f.write_str("memory state collection not supported"),
            Error::ReadMeminfo { path, source } => write!(f, "read meminfo: {}", goerr::path_error("open", path, source)),
            Error::NoMemTotal => f.write_str("meminfo: no MemTotal"),
            Error::SwapUsage(e) => write!(f, "sysctl vm.swapusage: {}", goerr::errno_text(e)),
            Error::VmStatistics(kr) => write!(f, "host_statistics64: kern_return={kr}"),
        }
    }
}

impl std::error::Error for Error {}

/// Where macOS memory readings come from, so recorded cases replay without a live host. The
/// real one is [`HostMemory`]; [`FixedMemory`] answers from a recorded sample.
pub trait MemorySource: Send + Sync {
    /// One raw macOS reading.
    fn darwin(&self) -> Result<DarwinSample, Error>;
    /// The kernel page size, which the Linux sample reports.
    fn page_size(&self) -> u64;
}

/// The live host, through `spectra_sys`: Mach VM statistics and sysctls on macOS. Elsewhere
/// [`MemorySource::darwin`] is [`Error::NotSupported`], as in Go's non-cgo build.
#[derive(Clone, Copy, Debug, Default)]
pub struct HostMemory;

impl MemorySource for HostMemory {
    #[cfg(target_os = "macos")]
    fn darwin(&self) -> Result<DarwinSample, Error> {
        use spectra_sys::darwin as sys;
        let mut s = DarwinSample {
            page_size_bytes: sys::page_size(),
            physical_bytes: sys::sysctl_u64("hw.memsize").unwrap_or(0),
            ..DarwinSample::default()
        };
        let swap = sys::swap_usage().map_err(Error::SwapUsage)?;
        s.swap = SwapUsage { total_bytes: swap.total, used_bytes: swap.used, free_bytes: swap.avail, encrypted: swap.encrypted };
        let vm = sys::vm_statistics64().map_err(Error::VmStatistics)?;
        s.vm = DarwinVmCounts {
            wire: u64::from(vm.wire_count),
            active: u64::from(vm.active_count),
            inactive: u64::from(vm.inactive_count),
            speculative: u64::from(vm.speculative_count),
            free: u64::from(vm.free_count),
            purgeable: u64::from(vm.purgeable_count),
            external: u64::from(vm.external_page_count),
            internal: u64::from(vm.internal_page_count),
            compressor: u64::from(vm.compressor_page_count),
            uncompressed_compressor: vm.total_uncompressed_pages_in_compressor,
            compressions: vm.compressions,
            decompressions: vm.decompressions,
            swapins: vm.swapins,
            swapouts: vm.swapouts,
        };
        s.pressure_level = sys::sysctl_u32("kern.memorystatus_vm_pressure_level").ok();
        s.memorystatus_level = sys::sysctl_u32("kern.memorystatus_level").ok();
        Ok(s)
    }

    #[cfg(not(target_os = "macos"))]
    fn darwin(&self) -> Result<DarwinSample, Error> {
        Err(Error::NotSupported)
    }

    #[cfg(unix)]
    fn page_size(&self) -> u64 {
        spectra_sys::page_size()
    }

    #[cfg(not(unix))]
    fn page_size(&self) -> u64 {
        0
    }
}

/// A recorded reading: `darwin` answers [`MemorySource::darwin`] ([`Error::NotSupported`] when
/// `None`), and `page_size` the Linux page size.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct FixedMemory {
    pub darwin: Option<DarwinSample>,
    pub page_size: u64,
}

impl MemorySource for FixedMemory {
    fn darwin(&self) -> Result<DarwinSample, Error> {
        self.darwin.ok_or(Error::NotSupported)
    }

    fn page_size(&self) -> u64 {
        self.page_size
    }
}

/// Default Linux procfs root.
pub const PROC_ROOT: &str = "/proc";

/// What [`collect`] reads from.
pub struct Options<'a> {
    /// The OS to collect for; [`Kind::Unknown`] means the host.
    pub os: Kind,
    pub fs: &'a dyn Fs,
    pub clock: &'a dyn Clock,
    pub source: &'a dyn MemorySource,
    /// The Linux procfs root; empty means [`PROC_ROOT`].
    pub proc_root: String,
    /// Overrides the Linux page size; 0 means the source's.
    pub page_size: u64,
}

/// Go's `CollectWithOptions`: the current host memory state for `opts.os`.
pub fn collect(opts: &Options<'_>) -> Result<MemoryState, Error> {
    match opts.os.resolve() {
        Kind::Darwin => {
            let sample = opts.source.darwin()?;
            Ok(from_darwin_sample(&sample, GoTime::utc(opts.clock.now())))
        }
        Kind::Linux => linux::collect(opts),
        Kind::Unknown => Err(Error::NotSupported),
    }
}

/// Go's `FromDarwinSample`: a macOS reading assembled into a [`MemoryState`].
pub fn from_darwin_sample(s: &DarwinSample, collected_at: GoTime) -> MemoryState {
    let page = s.page_size_bytes;
    let vm = &s.vm;
    let mut ms = MemoryState {
        page_size_bytes: page,
        collected_at,
        pressure_level: PressureLevel::Unknown,
        physical_bytes: s.physical_bytes,
        swap: s.swap,
        wired: vm.wire.wrapping_mul(page),
        active: vm.active.wrapping_mul(page),
        inactive: vm.inactive.wrapping_mul(page),
        speculative: vm.speculative.wrapping_mul(page),
        free: vm.free.wrapping_mul(page),
        purgeable: vm.purgeable.wrapping_mul(page),
        file_backed: vm.external.wrapping_mul(page),
        anonymous: vm.internal.wrapping_mul(page),
        compressor_occupied: vm.compressor.wrapping_mul(page),
        compressor_stored: vm.uncompressed_compressor.wrapping_mul(page),
        compressions: vm.compressions,
        decompressions: vm.decompressions,
        swap_ins: vm.swapins,
        swap_outs: vm.swapouts,
        ..MemoryState::default()
    };
    if let Some(raw) = s.pressure_level {
        ms.pressure_level = PressureLevel::from_raw(u64::from(raw));
    }
    finish(&mut ms);
    if let Some(level) = s.memorystatus_level {
        apply_memorystatus_level(&mut ms, level);
    }
    ms
}

/// Go's `finish`: the compressor ratio, and the page-fraction free percentage that
/// `pressure_free_percent` falls back to.
fn finish(ms: &mut MemoryState) {
    if ms.compressor_occupied > 0 {
        ms.compressor_ratio = ms.compressor_stored as f64 / ms.compressor_occupied as f64;
    }
    if ms.physical_bytes > 0 {
        let free = ms.free.wrapping_add(ms.speculative);
        ms.free_pages_percent = free as f64 / ms.physical_bytes as f64 * 100.0;
        ms.pressure_free_percent = ms.free_pages_percent;
    }
}

/// Go's `applyMemorystatusLevel` (PR #755): `kern.memorystatus_level` replaces the page-fraction
/// fallback, unless it reads above 100.
pub fn apply_memorystatus_level(ms: &mut MemoryState, level: u32) {
    if level <= 100 {
        ms.pressure_free_percent = f64::from(level);
    }
}

#[cfg(test)]
mod tests;
