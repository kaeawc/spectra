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
}

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
    fn rooted_fs_refuses_to_leave_the_root() {
        let fs = RootedFs::new("/nonexistent-root");
        let err = fs.read(Path::new("/proc/../../etc/passwd")).expect_err("escape");
        assert_eq!(err.kind(), io::ErrorKind::InvalidInput);
    }
}
