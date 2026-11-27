//! Spectra's foreign-function calls: the one crate allowed `unsafe` code. Each function wraps one
//! OS call in a safe signature that returns plain Rust values or `io::Error`, so collectors in
//! other crates stay `#![forbid(unsafe_code)]`.
//!
//! macOS: sysctl by name, swap usage, Mach VM statistics, libproc rusage, path and BSD name.
//! Linux collectors read `/proc` and `/sys` through `spectra_base::fs::Fs` and need no FFI.

#[cfg(target_os = "macos")]
pub mod darwin;
