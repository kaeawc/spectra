//! Go's `debug/buildinfo.Read` for thin Mach-O and ELF executables, down to the main module
//! path the classifier uses (`bi.Main.Path`, else `bi.Path`). Both the inline (Go 1.18+) and the
//! older pointer-based layouts are read. Universal, PE, XCOFF and Plan 9 files have no module.

use super::reader::{EOF, ReadAt, Section, UNEXPECTED_EOF, read_data_at};
use super::{elf, macho};

const MAGIC: &[u8] = b"\xff Go buildinf:";
const ALIGN: u64 = 16;
const HEADER_SIZE: u64 = 32;
const SEARCH_CHUNK: u64 = 1 << 20;

/// A segment or program header: where `[addr, addr+filesz)` lives in the file.
struct Region {
    addr: u64,
    filesz: u64,
    offset: u64,
    skip: bool,
}

struct Exe<'a> {
    r: &'a dyn ReadAt,
    regions: Vec<Region>,
    data_start: (u64, u64),
}

impl<'a> Exe<'a> {
    /// Go's `DataReader(addr)`: the rest of the region holding `addr`.
    fn reader(&self, addr: u64) -> Option<Section<'a>> {
        for g in &self.regions {
            if g.addr <= addr && addr <= g.addr.wrapping_add(g.filesz).wrapping_sub(1) {
                if g.skip {
                    continue;
                }
                let remaining = g.addr.wrapping_add(g.filesz).wrapping_sub(addr);
                return Some(Section { r: self.r, off: g.offset.wrapping_add(addr - g.addr), size: remaining });
            }
        }
        None
    }

    /// Go's `readData`: `size` bytes at `addr`, or an error (`EOF` when there are fewer).
    fn read(&self, addr: u64, size: u64) -> Result<Vec<u8>, String> {
        let r = self.reader(addr).ok_or("unrecognized file format")?;
        read_data_at(&r, size, 0)
    }
}

/// The module path recorded in a Go executable, or "" when there is none.
pub fn go_module(r: &dyn ReadAt) -> String {
    let Some(modinfo) = raw_mod(r) else { return String::new() };
    let Some((path, main)) = parse_build_info(&modinfo) else { return String::new() };
    if main.is_empty() { path } else { main }
}

fn exe(r: &dyn ReadAt) -> Option<Exe<'_>> {
    let mut ident = [0u8; 16];
    if r.read_at(0, &mut ident).ok()? < 16 {
        return None;
    }
    if ident.starts_with(b"\x7fELF") {
        let f = elf::parse(r).ok()?;
        let regions = f.progs.iter().map(|p| Region { addr: p.vaddr, filesz: p.filesz, offset: p.off, skip: false }).collect();
        let data_start = f.section(".go.buildinfo").map(|s| (s.addr, s.size)).or_else(|| {
            f.progs.iter().find(|p| p.typ == elf::PT_LOAD && p.flags & (elf::PF_X | elf::PF_W) == elf::PF_W).map(|p| (p.vaddr, p.memsz))
        });
        return Some(Exe { r, regions, data_start: data_start.unwrap_or((0, 0)) });
    }
    if ident.starts_with(b"\xfe\xed\xfa") || ident[1..].starts_with(b"\xfa\xed\xfe") {
        let f = macho::parse(r).ok()?;
        let regions =
            f.segments().map(|s| Region { addr: s.addr, filesz: s.filesz, offset: s.offset, skip: s.name == "__PAGEZERO" }).collect();
        let data_start = f.sections.iter().find(|s| s.name == "__go_buildinfo").map(|s| (s.addr, s.size)).or_else(|| {
            const RW: u32 = 3;
            f.segments().find(|s| s.addr != 0 && s.filesz != 0 && s.prot == RW && s.maxprot == RW).map(|s| (s.addr, s.memsz))
        });
        return Some(Exe { r, regions, data_start: data_start.unwrap_or((0, 0)) });
    }
    None
}

/// Go's `readRawBuildInfo`, returning only the module info (sentinels stripped).
fn raw_mod(r: &dyn ReadAt) -> Option<String> {
    let x = exe(r)?;
    let (start, size) = x.data_start;
    if size == 0 {
        return None;
    }
    let addr = search_magic(&x, start, size)?;
    let header = x.read(addr, HEADER_SIZE).ok()?;
    let flags = header[15];
    let (vers, modinfo) = if flags & 2 == 2 {
        let (vers, next) = decode_string(&x, addr + HEADER_SIZE)?;
        let (m, _) = decode_string(&x, next)?;
        (vers, m)
    } else {
        let ptr_size = usize::from(header[14]);
        let big = flags & 1 == 1;
        let read_ptr = |b: &[u8]| -> u64 {
            match (ptr_size, big) {
                (4, false) => u64::from(u32::from_le_bytes([b[0], b[1], b[2], b[3]])),
                (4, true) => u64::from(u32::from_be_bytes([b[0], b[1], b[2], b[3]])),
                (_, false) => u64::from_le_bytes([b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7]]),
                (_, true) => u64::from_be_bytes([b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7]]),
            }
        };
        if ptr_size != 4 && ptr_size != 8 {
            return None;
        }
        let vers = read_string(&x, ptr_size, &read_ptr, read_ptr(&header[16..]));
        let m = read_string(&x, ptr_size, &read_ptr, read_ptr(&header[16 + ptr_size..]));
        (vers, m)
    };
    if vers.is_empty() {
        return None;
    }
    let b = modinfo.as_bytes();
    if b.len() >= 33 && b[b.len() - 17] == b'\n' {
        Some(String::from_utf8_lossy(&b[16..b.len() - 16]).into_owned())
    } else {
        Some(String::new())
    }
}

/// Go's `searchMagic`: the 16-aligned address of the build-info header in the data range.
fn search_magic(x: &Exe<'_>, start: u64, size: u64) -> Option<u64> {
    let end = start.checked_add(size)?;
    let mut start = start.checked_add(ALIGN - 1)? & !(ALIGN - 1);
    if start >= end {
        return None;
    }
    while start < end {
        let remaining = end - start;
        let chunk = SEARCH_CHUNK.min(remaining);
        let mut buf = vec![0u8; chunk as usize];
        let r = x.reader(start)?;
        let n = r.read_at(0, &mut buf).ok()?;
        if n == 0 {
            return None;
        }
        // Offsets stay relative to the current window, as in Go (whose return after skipping
        // an unaligned match is relative to the skipped-to window too).
        let mut data = &buf[..n];
        while !data.is_empty() {
            let Some(i) = data.windows(MAGIC.len()).position(|w| w == MAGIC) else { break };
            if remaining - (i as u64) < HEADER_SIZE {
                return None;
            }
            if !(i as u64).is_multiple_of(ALIGN) {
                let next = (i + ALIGN as usize - 1) & !(ALIGN as usize - 1);
                if next > data.len() {
                    return None;
                }
                data = &data[next..];
                continue;
            }
            return Some(start + i as u64);
        }
        start += chunk;
    }
    None
}

/// Go's `decodeString`: a uvarint length and that many bytes.
fn decode_string(x: &Exe<'_>, addr: u64) -> Option<(String, u64)> {
    let b = x.read(addr, 10).ok()?;
    let (length, n) = uvarint(&b)?;
    let addr = addr + n as u64;
    let data = match x.read(addr, length) {
        Ok(d) => d,
        Err(e) if e == EOF || e == UNEXPECTED_EOF => return None,
        Err(_) => return None,
    };
    if (data.len() as u64) < length {
        return None;
    }
    Some((String::from_utf8_lossy(&data).into_owned(), addr + length))
}

/// Go's `readString` for the pointer-based layout: a (pointer, length) header.
fn read_string(x: &Exe<'_>, ptr_size: usize, read_ptr: &dyn Fn(&[u8]) -> u64, addr: u64) -> String {
    let Ok(hdr) = x.read(addr, 2 * ptr_size as u64) else { return String::new() };
    let (data_addr, data_len) = (read_ptr(&hdr), read_ptr(&hdr[ptr_size..]));
    match x.read(data_addr, data_len) {
        Ok(d) if d.len() as u64 >= data_len => String::from_utf8_lossy(&d).into_owned(),
        _ => String::new(),
    }
}

/// Go's `binary.Uvarint`; `None` for Go's `n <= 0`.
fn uvarint(b: &[u8]) -> Option<(u64, usize)> {
    let (mut x, mut s) = (0u64, 0u32);
    for (i, &c) in b.iter().enumerate() {
        if i == 10 {
            return None;
        }
        if c < 0x80 {
            if i == 9 && c > 1 {
                return None;
            }
            return Some((x | u64::from(c) << s, i + 1));
        }
        x |= u64::from(c & 0x7f) << s;
        s += 7;
    }
    None
}

/// Go's `debug.ParseBuildInfo`, keeping `path` and the main module path; `None` where Go fails.
/// `build` lines are checked only for a key and `=`, not for Go's quoting rules.
fn parse_build_info(data: &str) -> Option<(String, String)> {
    let module = |rest: &str| -> Option<String> {
        let elem: Vec<&str> = rest.split('\t').collect();
        (elem.len() == 2 || elem.len() == 3).then(|| elem[0].to_string())
    };
    let (mut path, mut main) = (String::new(), String::new());
    let mut have_last = false;
    let mut data = data;
    while let Some((line, rest)) = data.split_once('\n') {
        data = rest;
        if let Some(p) = line.strip_prefix("path\t") {
            path = p.to_string();
        } else if let Some(m) = line.strip_prefix("mod\t") {
            main = module(m)?;
            have_last = true;
        } else if let Some(d) = line.strip_prefix("dep\t") {
            module(d)?;
            have_last = true;
        } else if let Some(r) = line.strip_prefix("=>\t") {
            if r.split('\t').count() != 3 || !have_last {
                return None;
            }
            have_last = false;
        } else if let Some(kv) = line.strip_prefix("build\t")
            && (kv.is_empty() || kv.starts_with('=') || !kv.contains('='))
        {
            return None;
        }
    }
    Some((path, main))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_build_info_reads_path_and_main() {
        let info =
            "path\texample.com/cmd/x\nmod\texample.com\tv1.2.3\th1:abc=\ndep\tgolang.org/x/sys\tv0.1.0\th1:x=\nbuild\t-compiler=gc\n";
        assert_eq!(parse_build_info(info), Some(("example.com/cmd/x".into(), "example.com".into())));
        assert_eq!(parse_build_info("mod\tbad\n"), None, "a module line needs 2 or 3 columns");
        assert_eq!(parse_build_info("=>\ta\tb\tc\n"), None, "a replacement needs a module before it");
        assert_eq!(parse_build_info("path\tonly"), Some((String::new(), String::new())), "a line without a newline is not read");
    }

    #[test]
    fn uvarint_matches_go() {
        assert_eq!(uvarint(&[0x05]), Some((5, 1)));
        assert_eq!(uvarint(&[0xac, 0x02]), Some((300, 2)));
        assert_eq!(uvarint(&[0x80]), None);
        assert_eq!(uvarint(&[]), None);
    }
}
