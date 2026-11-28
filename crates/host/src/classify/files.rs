//! The classifier's file access (Go's `FileSystem` over `OSFileSystem`): opening only regular
//! files, bounded whole-file reads, `filepath.EvalSymlinks`, and `FileMode.Type()` text.

use std::path::Path;

use spectra_base::fs::{FileInfo, FileType, Fs};

use super::gopath::{clean, dir, is_abs, join};
use super::reader::FileReader;
use crate::goerr;

/// Go's `ErrNotRegular` text.
pub const NOT_REGULAR: &str = "not a regular file";
/// Go's `ErrTooLarge` text.
pub const TOO_LARGE: &str = "file exceeds read limit";

/// Go's `OSFileSystem.Open`: `path` only if it is a regular file, read through positioned reads
/// (which open without blocking on a FIFO swapped in after this check).
pub fn open_regular<'a>(fs: &'a dyn Fs, path: &str) -> Result<FileReader<'a>, String> {
    let fi = fs.metadata(Path::new(path)).map_err(|e| goerr::path_error("stat", path, &e))?;
    if fi.file_type != FileType::File {
        return Err(format!("open {path}: {NOT_REGULAR}"));
    }
    Ok(FileReader { fs, path: path.to_string() })
}

/// Go's `OSFileSystem.ReadFile(path, limit)`: at most `limit` bytes, failing when the file is
/// larger.
pub fn read_file(fs: &dyn Fs, path: &str, limit: u64) -> Result<Vec<u8>, String> {
    let fi = fs.metadata(Path::new(path)).map_err(|e| goerr::path_error("stat", path, &e))?;
    if fi.file_type != FileType::File {
        return Err(format!("open {path}: {NOT_REGULAR}"));
    }
    if fi.len > limit {
        return Err(format!("{path}: {TOO_LARGE} ({limit} bytes)"));
    }
    let mut buf = vec![0u8; usize::try_from(fi.len.min(limit) + 1).unwrap_or(usize::MAX)];
    let n = fs.read_at(Path::new(path), 0, &mut buf).map_err(|e| goerr::path_error("read", path, &e))?;
    if n as u64 > limit {
        return Err(format!("{path}: {TOO_LARGE} ({limit} bytes)"));
    }
    buf.truncate(n);
    Ok(buf)
}

/// Go's `FileMode.Type().String()` for what `fi` is: `d---------`, `p---------`...
pub fn mode_type(fi: &FileInfo) -> String {
    let c = match fi.file_type {
        FileType::Dir => 'd',
        FileType::Symlink => 'L',
        FileType::File => '-',
        FileType::Other => match fi.mode & 0o170000 {
            0o010000 => 'p',
            0o140000 => 'S',
            0o020000 => return "Dc---------".into(),
            0o060000 => 'D',
            _ => '?',
        },
    };
    format!("{c}---------")
}

/// Go's `filepath.EvalSymlinks` (Unix `walkSymlinks`): `path` with every symlink resolved,
/// lexically cleaned; an error when a component is missing.
pub fn eval_symlinks(fs: &dyn Fs, path: &str) -> Option<String> {
    let mut dest = if is_abs(path) { "/".to_string() } else { String::new() };
    let mut rest = path.to_string();
    let mut links = 0;
    loop {
        let trimmed = rest.trim_start_matches('/');
        if trimmed.is_empty() {
            break;
        }
        let (elem, after) = trimmed.split_once('/').unwrap_or((trimmed, ""));
        let (elem, after) = (elem.to_string(), after.to_string());
        rest = after;
        match elem.as_str() {
            "." => continue,
            ".." => {
                dest = if dest.is_empty() || dest == "/" { if dest == "/" { dest } else { "..".into() } } else { dir(&dest) };
                continue;
            }
            _ => {}
        }
        let candidate = if dest.is_empty() { elem.clone() } else { join(&[&dest, &elem]) };
        let fi = fs.symlink_metadata(Path::new(&candidate)).ok()?;
        if fi.file_type != FileType::Symlink {
            if fi.file_type != FileType::Dir && !rest.trim_start_matches('/').is_empty() {
                return None; // ENOTDIR
            }
            dest = candidate;
            continue;
        }
        links += 1;
        if links > 255 {
            return None;
        }
        let target = fs.read_link(Path::new(&candidate)).ok()?.to_string_lossy().into_owned();
        rest = format!("{target}/{rest}");
        if is_abs(&target) {
            dest = "/".into();
        }
    }
    Some(clean(if dest.is_empty() { "." } else { &dest }))
}
