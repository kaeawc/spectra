//! A sample of system-wide energy per SoC subsystem (Go's `power_ioreport.go`). Only the type
//! is ported: the Apple Silicon IOReport sampler (`power_ioreport_darwin_arm64.go`, which needs
//! CoreFoundation FFI) is a follow-up, so nothing here produces one yet.

use serde::Serialize;
use spectra_base::gojson::GoDuration;

/// Energy consumed by each SoC subsystem over `interval`; every joule field is non-negative.
/// `package_joules` is the sum of the per-subsystem joules of the IOReport "Energy Model" group
/// and approximates what `powermetrics` calls "Combined Power" on Apple Silicon.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize)]
pub struct SocPower {
    #[serde(rename = "interval_ns")]
    pub interval: GoDuration,
    pub cpu_p_joules: f64,
    pub cpu_e_joules: f64,
    pub gpu_joules: f64,
    pub ane_joules: f64,
    pub dram_joules: f64,
    pub package_joules: f64,
}

impl SocPower {
    /// Go's `Watts`: the average package power across the sample window; 0 for a window that
    /// is not positive.
    pub fn watts(&self) -> f64 {
        if self.interval.0 <= 0 {
            return 0.0;
        }
        // Go's Duration.Seconds: whole seconds plus the remainder as a fraction.
        let (sec, nsec) = (self.interval.0 / 1_000_000_000, self.interval.0 % 1_000_000_000);
        self.package_joules / (sec as f64 + nsec as f64 / 1e9)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // Go: TestSoCPowerWatts.
    #[test]
    fn watts_table() {
        let p = |joules, interval| SocPower { package_joules: joules, interval: GoDuration(interval), ..SocPower::default() };
        assert_eq!(p(8.6, 1_000_000_000).watts(), 8.6);
        assert_eq!(p(4.0, 500_000_000).watts(), 8.0);
        assert_eq!(p(1.0, 0).watts(), 0.0);
        assert_eq!(p(1.0, -5).watts(), 0.0);
    }
}
