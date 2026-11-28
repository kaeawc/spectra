//! Spectra's cross-platform host collectors. Each takes its seams (filesystem, clock,
//! subprocesses, and for macOS memory a [`memstate::MemorySource`]) and an OS
//! [`spectra_base::hostos::Kind`], so the Linux and macOS paths both run, against fixtures, on
//! any host.
//!
//! - [`memstate`]: memory pressure, compressor and swap (Go's `internal/memstate`).
//! - [`sysload`]: the memory-pressure and swap summary triage ranks (Go's `internal/sysload`).
//! - [`pressure`]: Linux PSI and cgroup v2 accounting (Go's `internal/pressure`).
//! - [`procdiag`]: why a Linux process is slow or stuck, from two `/proc` samples (Go's
//!   `internal/procdiag`).
//! - [`process`]: the process table, families, trees and argv (Go's `internal/process`), with
//!   macOS facts through a [`process::ProcessSource`].
//! - [`lineage`]: a process's ancestors and descendant subtree (Go's `internal/lineage`).
//! - [`localtz`]: the local time zone, chosen as Go chooses `time.Local`.
//! - [`gosort`]: Go's unstable `sort.Slice`, for ties that must land where Go puts them.

pub mod classify;
pub mod goerr;
pub mod gosort;
pub mod lineage;
pub mod localtz;
pub mod memstate;
pub mod pressure;
pub mod procdiag;
pub mod process;
pub mod sysload;

#[cfg(test)]
mod testfs;
