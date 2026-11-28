//! Positioned reads with Go's `io.ReaderAt` semantics, over which the Mach-O, ELF and build-info
//! readers work. Errors are Go's texts (`EOF`, `unexpected EOF`, `os.PathError`), because they
//! end up in evidence strings.

use std::io;
use std::path::Path;

use spectra_base::fs::Fs;

use crate::goerr;

/// Go's `io.EOF` text.
pub const EOF: &str = "EOF";
/// Go's `io.ErrUnexpectedEOF` text.
pub const UNEXPECTED_EOF: &str = "unexpected EOF";

/// Go's `io.ReaderAt`: `Ok(n)` with `n < buf.len()` means the data ended there (Go returns
/// `io.EOF` with it); `Err` carries the Go error text.
pub trait ReadAt: Sync {
    fn read_at(&self, off: u64, buf: &mut [u8]) -> Result<usize, String>;
}

impl ReadAt for [u8] {
    fn read_at(&self, off: u64, buf: &mut [u8]) -> Result<usize, String> {
        let start = usize::try_from(off).unwrap_or(usize::MAX).min(self.len());
        let n = (self.len() - start).min(buf.len());
        buf[..n].copy_from_slice(&self[start..start + n]);
        Ok(n)
    }
}

impl ReadAt for Vec<u8> {
    fn read_at(&self, off: u64, buf: &mut [u8]) -> Result<usize, String> {
        self.as_slice().read_at(off, buf)
    }
}

impl<T: ReadAt + ?Sized> ReadAt for &T {
    fn read_at(&self, off: u64, buf: &mut [u8]) -> Result<usize, String> {
        (**self).read_at(off, buf)
    }
}

/// One file read through [`Fs::read_at`]. Failures read as Go's `*os.PathError`: `open` for
/// what Go reports when opening (missing, permission, not a regular file), `read` otherwise.
pub struct FileReader<'a> {
    pub fs: &'a dyn Fs,
    pub path: String,
}

impl ReadAt for FileReader<'_> {
    fn read_at(&self, off: u64, buf: &mut [u8]) -> Result<usize, String> {
        self.fs.read_at(Path::new(&self.path), off, buf).map_err(|e| {
            let op = match e.kind() {
                io::ErrorKind::NotFound | io::ErrorKind::PermissionDenied | io::ErrorKind::InvalidInput => "open",
                _ => "read",
            };
            goerr::path_error(op, &self.path, &e)
        })
    }
}

/// Go's `io.NewSectionReader(r, off, size)`.
pub struct Section<'a> {
    pub r: &'a dyn ReadAt,
    pub off: u64,
    pub size: u64,
}

impl ReadAt for Section<'_> {
    fn read_at(&self, off: u64, buf: &mut [u8]) -> Result<usize, String> {
        if off >= self.size {
            return Ok(0);
        }
        let max = usize::try_from(self.size - off).unwrap_or(usize::MAX).min(buf.len());
        self.r.read_at(self.off.wrapping_add(off), &mut buf[..max])
    }
}

/// `r.ReadAt(buf, off)` that must fill `buf`: a short read is Go's `io.EOF`.
pub fn read_full_at(r: &dyn ReadAt, off: u64, buf: &mut [u8]) -> Result<(), String> {
    if r.read_at(off, buf)? < buf.len() { Err(EOF.to_string()) } else { Ok(()) }
}

/// Go's `saferio.ReadDataAt`: `n` bytes at `off`, read in 10 MiB chunks when large.
pub fn read_data_at(r: &dyn ReadAt, n: u64, off: u64) -> Result<Vec<u8>, String> {
    const CHUNK: u64 = 10 << 20;
    if i64::try_from(n).is_err() {
        return Err(UNEXPECTED_EOF.to_string());
    }
    if n < CHUNK {
        let mut buf = vec![0u8; n as usize];
        if n > 0 {
            read_full_at(r, off, &mut buf)?;
        }
        return Ok(buf);
    }
    let mut out = Vec::new();
    let (mut n, mut off) = (n, off);
    let mut chunk = vec![0u8; CHUNK as usize];
    while n > 0 {
        let next = n.min(CHUNK) as usize;
        read_full_at(r, off, &mut chunk[..next])?;
        out.extend_from_slice(&chunk[..next]);
        n -= next as u64;
        off += next as u64;
    }
    Ok(out)
}

/// A sequential reader over a byte slice with Go's `binary.Read` errors: nothing left is
/// `EOF`, too little is `unexpected EOF`.
pub struct Cursor<'a> {
    data: &'a [u8],
    pos: usize,
}

impl<'a> Cursor<'a> {
    pub fn new(data: &'a [u8]) -> Self {
        Cursor { data, pos: 0 }
    }

    /// The next `n` bytes.
    pub fn take(&mut self, n: usize) -> Result<&'a [u8], String> {
        let left = self.data.len() - self.pos;
        if n == 0 {
            return Ok(&self.data[self.pos..self.pos]);
        }
        if left == 0 {
            return Err(EOF.to_string());
        }
        if left < n {
            self.pos = self.data.len();
            return Err(UNEXPECTED_EOF.to_string());
        }
        let out = &self.data[self.pos..self.pos + n];
        self.pos += n;
        Ok(out)
    }
}

/// Byte order of a binary.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Endian {
    Little,
    Big,
}

impl Endian {
    pub fn u16(self, b: &[u8]) -> u16 {
        let a = [b[0], b[1]];
        match self {
            Endian::Little => u16::from_le_bytes(a),
            Endian::Big => u16::from_be_bytes(a),
        }
    }

    pub fn u32(self, b: &[u8]) -> u32 {
        let a = [b[0], b[1], b[2], b[3]];
        match self {
            Endian::Little => u32::from_le_bytes(a),
            Endian::Big => u32::from_be_bytes(a),
        }
    }

    pub fn u64(self, b: &[u8]) -> u64 {
        let a = [b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7]];
        match self {
            Endian::Little => u64::from_le_bytes(a),
            Endian::Big => u64::from_be_bytes(a),
        }
    }
}

/// Go's `cstring`: bytes up to the first NUL.
pub fn cstring(b: &[u8]) -> String {
    let end = b.iter().position(|&c| c == 0).unwrap_or(b.len());
    String::from_utf8_lossy(&b[..end]).into_owned()
}
