//! Crash-safe file writes (Go's `fsutil.WriteFileAtomic`): write a temp file in the same
//! directory, fsync it, then rename it over the target, so readers see the old file or the new
//! one, never half of either.

pub mod identity;

use std::fs::{File, OpenOptions};
use std::io::{self, BufWriter, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

/// Writes `data` to `path` atomically with permissions `mode`.
pub fn write_file_atomic(path: &Path, data: &[u8], mode: u32) -> io::Result<()> {
    write_atomic(path, mode, |f| f.write_all(data))
}

/// Like [`write_file_atomic`], streaming the content through `write` with buffering.
pub fn write_file_atomic_with(path: &Path, mode: u32, write: impl FnOnce(&mut dyn Write) -> io::Result<()>) -> io::Result<()> {
    write_atomic(path, mode, |f| {
        let mut bw = BufWriter::new(f);
        write(&mut bw)?;
        bw.flush()
    })
}

fn write_atomic(path: &Path, mode: u32, payload: impl FnOnce(&mut File) -> io::Result<()>) -> io::Result<()> {
    let (tmp_path, mut tmp) = create_temp(path, mode).map_err(|e| context("write tempfile", e))?;
    let result = (|| {
        // The umask may have trimmed the create mode; set it exactly, as Go's Chmod does.
        tmp.set_permissions(std::fs::Permissions::from_mode(mode)).map_err(|e| context("write tempfile", e))?;
        payload(&mut tmp).map_err(|e| context("write tempfile", e))?;
        tmp.sync_all().map_err(|e| context("sync tempfile", e))?;
        drop(tmp);
        std::fs::rename(&tmp_path, path).map_err(|e| context("rename tempfile", e))
    })();
    if result.is_err() {
        let _ = std::fs::remove_file(&tmp_path);
    }
    result
}

static TEMP_COUNTER: AtomicU64 = AtomicU64::new(0);

/// Creates `.<name>.tmp-<pid>-<n>` next to `path`, retrying on a name collision.
fn create_temp(path: &Path, mode: u32) -> io::Result<(PathBuf, File)> {
    let dir = match path.parent() {
        Some(d) if !d.as_os_str().is_empty() => d,
        _ => Path::new("."),
    };
    let base = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    loop {
        let n = TEMP_COUNTER.fetch_add(1, Ordering::Relaxed);
        let tmp = dir.join(format!(".{base}.tmp-{}-{n}", std::process::id()));
        match OpenOptions::new().write(true).create_new(true).mode(mode).open(&tmp) {
            Ok(f) => return Ok((tmp, f)),
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(e) => return Err(e),
        }
    }
}

fn context(what: &str, e: io::Error) -> io::Error {
    io::Error::new(e.kind(), format!("{what}: {e}"))
}

/// A fresh, empty directory under the system temp dir for a test.
#[cfg(test)]
pub(crate) fn test_dir(name: &str) -> PathBuf {
    let n = TEMP_COUNTER.fetch_add(1, Ordering::Relaxed);
    let dir = std::env::temp_dir().join(format!("spectra-base-{name}-{}-{n}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("test dir");
    dir
}

#[cfg(test)]
#[allow(clippy::disallowed_methods)]
mod tests {
    use super::*;

    #[test]
    fn writes_content_and_mode_and_leaves_no_temp() {
        let dir = test_dir("atomic");
        let path = dir.join("out.json");
        write_file_atomic(&path, b"first", 0o600).expect("write");
        write_file_atomic(&path, b"second", 0o640).expect("overwrite");
        assert_eq!(std::fs::read(&path).expect("read"), b"second");
        assert_eq!(std::fs::metadata(&path).expect("meta").permissions().mode() & 0o777, 0o640);
        let names: Vec<_> = std::fs::read_dir(&dir).expect("dir").map(|e| e.expect("entry").file_name()).collect();
        assert_eq!(names, vec![std::ffi::OsString::from("out.json")]);
        std::fs::remove_dir_all(dir).expect("cleanup");
    }

    #[test]
    fn streams_through_a_writer() {
        let dir = test_dir("atomic-stream");
        let path = dir.join("out.txt");
        write_file_atomic_with(&path, 0o644, |w| w.write_all(b"a").and_then(|_| w.write_all(b"b"))).expect("write");
        assert_eq!(std::fs::read(&path).expect("read"), b"ab");
        std::fs::remove_dir_all(dir).expect("cleanup");
    }

    #[test]
    fn failed_payload_removes_the_temp_file() {
        let dir = test_dir("atomic-fail");
        let path = dir.join("out.txt");
        let err = write_file_atomic_with(&path, 0o644, |_| Err(io::Error::other("boom"))).expect_err("fails");
        assert!(err.to_string().contains("write tempfile: boom"));
        assert_eq!(std::fs::read_dir(&dir).expect("dir").count(), 0);
        std::fs::remove_dir_all(dir).expect("cleanup");
    }
}
