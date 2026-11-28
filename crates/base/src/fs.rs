//! Filesystem reads, behind a trait so tests can point a collector at a fixture tree.
//!
//! [`RootedFs`] serves absolute paths from a directory, so a Linux collector reading
//! `/proc/1/status` reads `<fixture>/proc/1/status` in a test, on any host.

use std::io;
use std::path::{Component, Path, PathBuf};

/// What a directory entry or a path is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FileType {
    File,
    Dir,
    Symlink,
    Other,
}

/// The parts of file metadata collectors use. `modified` and `ctime` are seconds and nanoseconds
/// since the Unix epoch. `dev`, `ino` and `ctime` feed [`crate::fsutil::identity::identity`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FileInfo {
    pub file_type: FileType,
    pub len: u64,
    pub mode: u32,
    pub modified: Option<(i64, u32)>,
    pub dev: u64,
    pub ino: u64,
    pub ctime: Option<(i64, u32)>,
}

/// One entry of a directory listing.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DirEntry {
    pub name: String,
    pub file_type: FileType,
}

/// Read-only filesystem access. Collectors take `&dyn Fs` instead of calling `std::fs`.
pub trait Fs: Send + Sync {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>>;
    /// Entries sorted by name; names that are not UTF-8 are skipped.
    fn read_dir(&self, path: &Path) -> io::Result<Vec<DirEntry>>;
    /// Follows symlinks.
    fn metadata(&self, path: &Path) -> io::Result<FileInfo>;
    /// Does not follow a final symlink.
    fn symlink_metadata(&self, path: &Path) -> io::Result<FileInfo>;
    fn read_link(&self, path: &Path) -> io::Result<PathBuf>;

    fn read_to_string(&self, path: &Path) -> io::Result<String> {
        String::from_utf8(self.read(path)?).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))
    }

    fn exists(&self, path: &Path) -> bool {
        self.metadata(path).is_ok()
    }

    /// Reads up to `buf.len()` bytes of the regular file at `path` from `offset` (a positioned
    /// read, like Go's `io.ReaderAt`); fewer bytes than asked means the file ended. Anything but
    /// a regular file is refused with [`not_regular`], without blocking on a FIFO. The default
    /// reads the whole file and slices it; [`OsFs`] reads only what is asked.
    fn read_at(&self, path: &Path, offset: u64, buf: &mut [u8]) -> io::Result<usize> {
        let data = self.read(path)?;
        let start = usize::try_from(offset).unwrap_or(usize::MAX).min(data.len());
        let n = (data.len() - start).min(buf.len());
        buf[..n].copy_from_slice(&data[start..start + n]);
        Ok(n)
    }
}

/// The error [`Fs::read_at`] returns for a path that is not a regular file. Its text is Go's
/// classify `ErrNotRegular`.
pub fn not_regular() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, "not a regular file")
}

/// `O_NONBLOCK`, so opening a FIFO returns at once instead of waiting for a writer.
#[cfg(target_os = "macos")]
const O_NONBLOCK: i32 = 0x0004;
#[cfg(not(target_os = "macos"))]
const O_NONBLOCK: i32 = 0o4000;

/// The real filesystem.
#[derive(Clone, Copy, Debug, Default)]
pub struct OsFs;

#[allow(clippy::disallowed_methods)] // the one place the real filesystem is read
impl Fs for OsFs {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        std::fs::read(path)
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<DirEntry>> {
        let mut out = Vec::new();
        for entry in std::fs::read_dir(path)? {
            let entry = entry?;
            let Ok(name) = entry.file_name().into_string() else { continue };
            out.push(DirEntry { name, file_type: file_type(entry.file_type()?) });
        }
        out.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(out)
    }

    fn metadata(&self, path: &Path) -> io::Result<FileInfo> {
        std::fs::metadata(path).map(|m| info(&m))
    }

    fn symlink_metadata(&self, path: &Path) -> io::Result<FileInfo> {
        std::fs::symlink_metadata(path).map(|m| info(&m))
    }

    fn read_link(&self, path: &Path) -> io::Result<PathBuf> {
        std::fs::read_link(path)
    }

    fn read_at(&self, path: &Path, offset: u64, buf: &mut [u8]) -> io::Result<usize> {
        use std::os::unix::fs::{FileExt, OpenOptionsExt};
        let f = std::fs::OpenOptions::new().read(true).custom_flags(O_NONBLOCK).open(path)?;
        // The open may race a path swapped after the caller's stat; check what was opened.
        if !f.metadata()?.is_file() {
            return Err(not_regular());
        }
        let mut n = 0;
        while n < buf.len() {
            match f.read_at(&mut buf[n..], offset.saturating_add(n as u64)) {
                Ok(0) => break,
                Ok(k) => n += k,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
                Err(e) => return Err(e),
            }
        }
        Ok(n)
    }
}

fn file_type(t: std::fs::FileType) -> FileType {
    if t.is_symlink() {
        FileType::Symlink
    } else if t.is_dir() {
        FileType::Dir
    } else if t.is_file() {
        FileType::File
    } else {
        FileType::Other
    }
}

fn info(m: &std::fs::Metadata) -> FileInfo {
    use std::os::unix::fs::MetadataExt;
    FileInfo {
        file_type: file_type(m.file_type()),
        len: m.len(),
        mode: m.mode(),
        modified: Some((m.mtime(), m.mtime_nsec() as u32)),
        dev: m.dev(),
        ino: m.ino(),
        ctime: Some((m.ctime(), m.ctime_nsec() as u32)),
    }
}

/// The real filesystem with absolute paths served from `root`: `/proc/1/stat` reads
/// `<root>/proc/1/stat`. Relative paths and `..` components that would leave the root are
/// refused.
#[derive(Clone, Debug)]
pub struct RootedFs {
    root: PathBuf,
}

impl RootedFs {
    pub fn new(root: impl Into<PathBuf>) -> Self {
        RootedFs { root: root.into() }
    }

    fn map(&self, path: &Path) -> io::Result<PathBuf> {
        let mut out = self.root.clone();
        let mut depth = 0usize;
        for c in path.components() {
            match c {
                Component::RootDir => {}
                Component::CurDir => {}
                Component::Normal(p) => {
                    out.push(p);
                    depth += 1;
                }
                Component::ParentDir if depth > 0 => {
                    out.pop();
                    depth -= 1;
                }
                Component::ParentDir | Component::Prefix(_) => {
                    return Err(io::Error::new(io::ErrorKind::InvalidInput, format!("{}: leaves the fixture root", path.display())));
                }
            }
        }
        Ok(out)
    }
}

impl Fs for RootedFs {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        OsFs.read(&self.map(path)?)
    }

    fn read_dir(&self, path: &Path) -> io::Result<Vec<DirEntry>> {
        OsFs.read_dir(&self.map(path)?)
    }

    fn metadata(&self, path: &Path) -> io::Result<FileInfo> {
        OsFs.metadata(&self.map(path)?)
    }

    fn symlink_metadata(&self, path: &Path) -> io::Result<FileInfo> {
        OsFs.symlink_metadata(&self.map(path)?)
    }

    fn read_link(&self, path: &Path) -> io::Result<PathBuf> {
        OsFs.read_link(&self.map(path)?)
    }

    fn read_at(&self, path: &Path, offset: u64, buf: &mut [u8]) -> io::Result<usize> {
        OsFs.read_at(&self.map(path)?, offset, buf)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::fsutil::write_file_atomic;

    fn fixture() -> PathBuf {
        let dir = crate::fsutil::test_dir("rootedfs");
        std::fs::create_dir_all(dir.join("proc/1")).expect("mkdir");
        write_file_atomic(&dir.join("proc/1/status"), b"Name:\tinit\n", 0o644).expect("write");
        write_file_atomic(&dir.join("proc/1/cmdline"), b"/sbin/init\0", 0o644).expect("write");
        dir
    }

    #[test]
    fn rooted_fs_serves_absolute_paths_from_the_root() {
        let root = fixture();
        let fs = RootedFs::new(&root);
        assert_eq!(fs.read_to_string(Path::new("/proc/1/status")).expect("read"), "Name:\tinit\n");
        let names: Vec<String> = fs.read_dir(Path::new("/proc/1")).expect("dir").into_iter().map(|e| e.name).collect();
        assert_eq!(names, vec!["cmdline", "status"]);
        assert_eq!(fs.metadata(Path::new("/proc/1")).expect("meta").file_type, FileType::Dir);
        assert!(!fs.exists(Path::new("/proc/2/status")));
        std::fs::remove_dir_all(root).expect("cleanup");
    }

    #[test]
    fn read_at_reads_a_window_and_refuses_non_regular_files() {
        let root = fixture();
        let fs = RootedFs::new(&root);
        let mut buf = [0u8; 4];
        assert_eq!(fs.read_at(Path::new("/proc/1/status"), 2, &mut buf).expect("read_at"), 4);
        assert_eq!(&buf, b"me:\t");
        assert_eq!(fs.read_at(Path::new("/proc/1/status"), 9, &mut buf).expect("tail"), 2, "short at the end");
        assert_eq!(fs.read_at(Path::new("/proc/1/status"), 99, &mut buf).expect("past"), 0);
        let err = fs.read_at(Path::new("/proc/1"), 0, &mut buf).expect_err("a directory");
        assert_eq!(err.to_string(), "not a regular file");
        assert!(fs.read_at(Path::new("/proc/2/status"), 0, &mut buf).is_err());
        std::fs::remove_dir_all(root).expect("cleanup");
    }

    /// The trait default serves implementations without a positioned read.
    #[test]
    fn read_at_default_slices_the_whole_file() {
        struct One;
        impl Fs for One {
            fn read(&self, _: &Path) -> io::Result<Vec<u8>> {
                Ok(b"abcdef".to_vec())
            }
            fn read_dir(&self, _: &Path) -> io::Result<Vec<DirEntry>> {
                Ok(Vec::new())
            }
            fn metadata(&self, _: &Path) -> io::Result<FileInfo> {
                Err(io::Error::other("none"))
            }
            fn symlink_metadata(&self, _: &Path) -> io::Result<FileInfo> {
                Err(io::Error::other("none"))
            }
            fn read_link(&self, _: &Path) -> io::Result<PathBuf> {
                Err(io::Error::other("none"))
            }
        }
        let mut buf = [0u8; 4];
        assert_eq!(One.read_at(Path::new("/x"), 4, &mut buf).expect("read_at"), 2);
        assert_eq!(&buf[..2], b"ef");
        assert_eq!(One.read_at(Path::new("/x"), u64::MAX, &mut buf).expect("past"), 0);
    }

    #[cfg(unix)]
    #[test]
    #[allow(clippy::disallowed_methods)]
    fn read_at_does_not_block_on_a_fifo() {
        let dir = crate::fsutil::test_dir("fifo");
        let fifo = dir.join("pipe");
        let made = std::process::Command::new("mkfifo").arg(&fifo).status().map(|s| s.success()).unwrap_or(false);
        if made {
            let err = OsFs.read_at(&fifo, 0, &mut [0u8; 4]).expect_err("fifo");
            assert_eq!(err.to_string(), "not a regular file");
        }
        std::fs::remove_dir_all(dir).expect("cleanup");
    }

    #[test]
    fn rooted_fs_refuses_to_leave_the_root() {
        let fs = RootedFs::new("/nonexistent-root");
        let err = fs.read(Path::new("/proc/../../etc/passwd")).expect_err("escape");
        assert_eq!(err.kind(), io::ErrorKind::InvalidInput);
    }
}
