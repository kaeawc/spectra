//! One process's argument vector, read from the kernel without touching the process: Linux
//! `/proc/<pid>/cmdline`, macOS `kern.procargs2` (Go's `argv.go`).

use std::path::Path;

use spectra_base::hostos::{Kind, Unsupported};

use super::Seams;
use crate::goerr;

/// Bounds on one process's argv (`kern.argmax` on macOS; Linux reads stop here). Past them the
/// flattened command line is used.
pub const MAX_ARGV_BYTES: usize = 1 << 20;
pub const MAX_ARGV_COUNT: usize = 4096;

/// Go's `argvFor`: `pid`'s argv when it still matches `full`, the listing's command line;
/// empty otherwise.
pub fn argv(s: &Seams<'_>, pid: i64, full: &str) -> Vec<String> {
    match host_argv(s, s.os.resolve(), "/proc", pid) {
        Ok(argv) if argv_matches(&argv, full) => argv,
        _ => Vec::new(),
    }
}

/// Go's `hostArgv`. Linux reads `<procfs>/<pid>/cmdline` through the seams' filesystem; macOS
/// asks the [`super::ProcessSource`].
pub fn host_argv(s: &Seams<'_>, kind: Kind, procfs: &str, pid: i64) -> Result<Vec<String>, String> {
    match kind {
        Kind::Linux => {
            let path = format!("{procfs}/{pid}/cmdline");
            let data = s.fs.read(Path::new(&path)).map_err(|e| goerr::path_error("open", &path, &e))?;
            if data.len() > MAX_ARGV_BYTES {
                return Err("cmdline exceeds argv bound".to_string());
            }
            Ok(split_cmdline(&data))
        }
        Kind::Darwin => s.source.argv(pid),
        Kind::Unknown => Err(Unsupported::new("process argv", kind.resolve()).to_string()),
    }
}

/// Go's `argvMatches`: argv is the process the listing saw. The listing's command line is argv
/// joined by spaces (whitespace runs collapsed on macOS); anything else is a recycled pid or a
/// rewritten argv.
pub fn argv_matches(argv: &[String], full: &str) -> bool {
    if argv.is_empty() {
        return false;
    }
    let joined = argv.join(" ");
    collapse(&joined) == collapse(full)
}

fn collapse(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// Go's `splitCmdline`: NUL-separated with a trailing NUL; empty (a kernel thread) is none.
pub fn split_cmdline(data: &[u8]) -> Vec<String> {
    let s = String::from_utf8_lossy(data);
    let s = s.trim_end_matches('\0');
    if s.is_empty() {
        return Vec::new();
    }
    let args: Vec<String> = s.split('\0').map(str::to_string).collect();
    if args.len() > MAX_ARGV_COUNT {
        return Vec::new();
    }
    args
}

/// Go's `parseProcArgs2`: a native-endian 32-bit argc, the exec path, NUL padding, then argc
/// NUL-terminated arguments (the environment that follows is not read).
pub fn parse_proc_args2(raw: &[u8]) -> Result<Vec<String>, String> {
    if raw.len() < 4 {
        return Err("procargs2: short response".to_string());
    }
    let n = u32::from_ne_bytes([raw[0], raw[1], raw[2], raw[3]]);
    if n == 0 {
        return Ok(Vec::new());
    }
    if n as usize > MAX_ARGV_COUNT {
        return Err(format!("procargs2: argc {n} exceeds bound"));
    }
    let argc = n as usize;
    let mut b = &raw[4..];
    let Some(end) = b.iter().position(|&c| c == 0) else {
        return Err("procargs2: missing executable terminator".to_string());
    };
    b = &b[end + 1..];
    while b.first() == Some(&0) {
        b = &b[1..];
    }
    let mut args = Vec::with_capacity(argc);
    while args.len() < argc {
        let Some(end) = b.iter().position(|&c| c == 0) else {
            return Err("procargs2: truncated argument".to_string());
        };
        args.push(String::from_utf8_lossy(&b[..end]).into_owned());
        b = &b[end + 1..];
    }
    Ok(args)
}
