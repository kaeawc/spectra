//! Spectra's foreign-function calls: the one crate allowed `unsafe` code. Each function wraps one
//! OS call in a safe signature that returns plain Rust values or `io::Error`, so collectors in
//! other crates stay `#![forbid(unsafe_code)]`.
//!
//! macOS: sysctl by name, swap usage, Mach VM statistics, libproc rusage, path and BSD name.
//! Linux collectors read `/proc` and `/sys` through `spectra_base::fs::Fs` and need no FFI beyond
//! [`page_size`].

#[cfg(target_os = "macos")]
pub mod darwin;

/// The kernel's page size in bytes (Go's `os.Getpagesize`), or 4096 when `sysconf` fails.
#[cfg(unix)]
pub fn page_size() -> u64 {
    // SAFETY: sysconf has no preconditions.
    let n = unsafe { libc::sysconf(libc::_SC_PAGESIZE) };
    u64::try_from(n).unwrap_or(4096)
}

#[cfg(all(test, unix))]
mod tests {
    #[test]
    fn page_size_is_a_power_of_two() {
        let p = super::page_size();
        assert!(p >= 4096 && p.is_power_of_two(), "{p}");
    }
}
