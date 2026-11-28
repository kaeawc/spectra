//! An in-memory [`Fs`] for unit tests: files at absolute paths, directories implied by them.

use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};

use spectra_base::fs::{DirEntry, FileInfo, FileType, Fs};

#[derive(Default)]
pub struct MapFs(BTreeMap<PathBuf, Vec<u8>>);

impl MapFs {
    pub fn new<'a>(files: impl IntoIterator<Item = (&'a str, &'a str)>) -> MapFs {
        MapFs(files.into_iter().map(|(p, c)| (PathBuf::from(p), c.as_bytes().to_vec())).collect())
    }

    fn info(file_type: FileType, len: u64) -> FileInfo {
        FileInfo { file_type, len, mode: 0o644, modified: None, dev: 0, ino: 0, ctime: None }
    }
}

fn not_found() -> io::Error {
    io::Error::from_raw_os_error(2)
}

impl Fs for MapFs {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        self.0.get(path).cloned().ok_or_else(not_found)
    }

    fn read_dir(&self, _path: &Path) -> io::Result<Vec<DirEntry>> {
        Err(io::Error::other("MapFs: read_dir is not supported"))
    }

    fn metadata(&self, path: &Path) -> io::Result<FileInfo> {
        if let Some(c) = self.0.get(path) {
            return Ok(MapFs::info(FileType::File, c.len() as u64));
        }
        if self.0.keys().any(|k| k.starts_with(path)) {
            return Ok(MapFs::info(FileType::Dir, 0));
        }
        Err(not_found())
    }

    fn symlink_metadata(&self, path: &Path) -> io::Result<FileInfo> {
        self.metadata(path)
    }

    fn read_link(&self, _path: &Path) -> io::Result<PathBuf> {
        Err(io::Error::other("MapFs: no symlinks"))
    }
}
