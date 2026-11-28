//! Selected kernel tunables, battery and power state, and per-process energy (Go's
//! `internal/sysinfo`). Every collector is read-only, local and user-privilege. See
//! `docs/design/system-inventory.md` for the schema.
//!
//! - [`sysctls`]: the allowlisted tunables, `sysctl(8)` on macOS and `/proc/sys` on Linux.
//! - [`fdlimit`]: the per-process file-descriptor limit `launchctl` reports.
//! - [`power`]: battery, thermal pressure, sleep assertions and top energy users (`pmset` and
//!   `top` on macOS, `/sys/class/power_supply` on Linux).
//! - [`rusage`]: per-pid energy accounting from two `proc_pid_rusage` snapshots.
//! - [`energy_impact`]: the documented per-pid impact score.
//! - [`tasks`]: `powermetrics --samplers tasks` plist rows.
//! - [`ioreport`]: the SoC power sample type. The Apple Silicon IOReport sampler itself is not
//!   ported yet.

pub mod energy_impact;
pub mod fdlimit;
pub mod ioreport;
pub mod power;
pub mod rusage;
pub mod sysctls;
pub mod tasks;
