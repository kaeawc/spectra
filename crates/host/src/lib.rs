//! Spectra's cross-platform host collectors. Each takes its seams (filesystem, clock,
//! subprocesses, and for macOS memory a [`memstate::MemorySource`]) and an OS
//! [`spectra_base::hostos::Kind`], so the Linux and macOS paths both run, against fixtures, on
//! any host.
//!
//! - [`memstate`]: memory pressure, compressor and swap (Go's `internal/memstate`).
//! - [`sysload`]: the memory-pressure and swap summary triage ranks (Go's `internal/sysload`).
//! - [`pressure`]: Linux PSI and cgroup v2 accounting (Go's `internal/pressure`).

pub mod goerr;
pub mod memstate;
pub mod pressure;
pub mod sysload;

#[cfg(test)]
mod testfs;
