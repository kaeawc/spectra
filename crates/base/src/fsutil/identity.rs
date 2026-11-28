//! File identity (Go's `fsutil.Identity`): the part of a stat result that changes whenever a file
//! is replaced or rewritten, even when size and mtime are preserved (`cp -p`, rsync, tar,
//! `touch -r`): the inode, and the ctime only the kernel sets.

use crate::fs::FileInfo;

/// Device, inode and ctime of a file. `ctime` is nanoseconds since the Unix epoch.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct FileId {
    pub dev: u64,
    pub ino: u64,
    pub ctime: i64,
}

/// Reads dev, inode and ctime from `info`; the zero [`FileId`] where `info` carries none.
pub fn identity(info: &FileInfo) -> FileId {
    let ctime = info.ctime.map_or(0, |(sec, nsec)| sec.wrapping_mul(1_000_000_000).wrapping_add(i64::from(nsec)));
    FileId { dev: info.dev, ino: info.ino, ctime }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::fs::{FileType, Fs, OsFs};
    use std::path::Path;

    fn info(dev: u64, ino: u64, ctime: Option<(i64, u32)>) -> FileInfo {
        FileInfo { file_type: FileType::File, len: 0, mode: 0o644, modified: None, dev, ino, ctime }
    }

    #[test]
    fn identity_reads_inode_and_ctime() {
        assert_eq!(identity(&info(7, 42, Some((3, 4)))), FileId { dev: 7, ino: 42, ctime: 3_000_000_004 });
        assert_eq!(identity(&info(0, 0, None)), FileId::default());
    }

    #[test]
    fn identity_of_a_real_file_has_inode_and_ctime() {
        let manifest = Path::new(env!("CARGO_MANIFEST_DIR")).join("Cargo.toml");
        let got = identity(&OsFs.metadata(&manifest).expect("stat"));
        assert!(got.ino != 0 && got.ctime != 0, "host identity = {got:?}");
    }
}
