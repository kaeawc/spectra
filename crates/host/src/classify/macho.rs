//! The parts of Go's `debug/macho.NewFile` the classifier uses: load commands, segments and
//! sections, LC_SYMTAB symbols, LC_DYSYMTAB validation and LC_LOAD_DYLIB names, with Go's
//! validation order and `FormatError` texts, read through positioned reads.

use super::reader::{Cursor, Endian, ReadAt, Section as SectionReader, cstring, read_data_at, read_full_at};

pub const MAGIC32: u32 = 0xfeed_face;
pub const MAGIC64: u32 = 0xfeed_facf;
pub const MAGIC_FAT: u32 = 0xcafe_babe;

pub const CPU_AMD64: u32 = 7 | 0x0100_0000;
pub const CPU_ARM64: u32 = 12 | 0x0100_0000;

const LC_SEGMENT: u32 = 0x1;
const LC_SYMTAB: u32 = 0x2;
const LC_DYSYMTAB: u32 = 0xb;
const LC_DYLIB: u32 = 0xc;
const LC_SEGMENT64: u32 = 0x19;
const LC_RPATH: u32 = 0x8000_001c;

/// Go's `macho.Cpu.String()`.
pub fn cpu_name(cpu: u32) -> String {
    match cpu {
        7 => "Cpu386".into(),
        CPU_AMD64 => "CpuAmd64".into(),
        12 => "CpuArm".into(),
        CPU_ARM64 => "CpuArm64".into(),
        18 => "CpuPpc".into(),
        0x0100_0012 => "CpuPpc64".into(),
        other => other.to_string(),
    }
}

/// Go's `macho.FormatError` text.
fn format_error(off: u64, msg: &str, val: Option<String>) -> String {
    let mut s = msg.to_string();
    if let Some(v) = val {
        s.push_str(&format!(" '{v}'"));
    }
    s.push_str(&format!(" in record at byte {off:#x}"));
    s
}

pub struct Segment {
    pub name: String,
    pub addr: u64,
    pub memsz: u64,
    pub offset: u64,
    pub filesz: u64,
    pub maxprot: u32,
    pub prot: u32,
}

pub struct Section {
    pub name: String,
    pub seg: String,
    pub addr: u64,
    pub size: u64,
    pub offset: u32,
}

pub struct Symbol {
    pub name: String,
    pub typ: u8,
}

pub enum Load {
    Segment(Segment),
    Dylib(String),
    Other,
}

pub struct File {
    pub loads: Vec<Load>,
    pub sections: Vec<Section>,
    /// `None` without an LC_SYMTAB (Go's nil `Symtab`).
    pub symtab: Option<Vec<Symbol>>,
}

impl File {
    /// Go's `File.Segment(name)`.
    pub fn segment(&self, name: &str) -> Option<&Segment> {
        self.segments().find(|s| s.name == name)
    }

    pub fn segments(&self) -> impl Iterator<Item = &Segment> {
        self.loads.iter().filter_map(|l| match l {
            Load::Segment(s) => Some(s),
            _ => None,
        })
    }

    /// Go's `ImportedLibraries`: every LC_LOAD_DYLIB name.
    pub fn imported_libraries(&self) -> Vec<String> {
        self.loads
            .iter()
            .filter_map(|l| match l {
                Load::Dylib(n) => Some(n.clone()),
                _ => None,
            })
            .collect()
    }

    /// A reader over `s`'s bytes in the file (Go's `Section.Open`).
    pub fn section_reader<'a>(r: &'a dyn ReadAt, s: &Section) -> SectionReader<'a> {
        SectionReader { r, off: u64::from(s.offset), size: s.size }
    }
}

/// Go's `macho.NewFile(r)`.
pub fn parse(r: &dyn ReadAt) -> Result<File, String> {
    let mut ident = [0u8; 4];
    read_full_at(r, 0, &mut ident)?;
    let be = u32::from_be_bytes(ident);
    let le = u32::from_le_bytes(ident);
    let (bo, magic) = if be & !1 == MAGIC32 {
        (Endian::Big, be)
    } else if le & !1 == MAGIC32 {
        (Endian::Little, le)
    } else {
        return Err(format_error(0, "invalid magic number", None));
    };
    // binary.Read of the 28-byte FileHeader from the start.
    let mut hdr = [0u8; 28];
    let n = r.read_at(0, &mut hdr)?;
    if n < hdr.len() {
        return Err(if n == 0 { super::reader::EOF } else { super::reader::UNEXPECTED_EOF }.to_string());
    }
    let ncmd = bo.u32(&hdr[16..]);
    let cmdsz = bo.u32(&hdr[20..]);
    let mut offset: u64 = if magic == MAGIC64 { 32 } else { 28 };
    let dat = read_data_at(r, u64::from(cmdsz), offset)?;
    let mut f = File { loads: Vec::new(), sections: Vec::new(), symtab: None };
    let mut dat: &[u8] = &dat;
    for _ in 0..ncmd {
        if dat.len() < 8 {
            return Err(format_error(offset, "command block too small", None));
        }
        let (cmd, siz) = (bo.u32(&dat[0..]), bo.u32(&dat[4..]));
        if siz < 8 || siz as usize > dat.len() {
            return Err(format_error(offset, "invalid command block size", None));
        }
        let cmddat = &dat[..siz as usize];
        dat = &dat[siz as usize..];
        offset += u64::from(siz);
        load_command(r, &mut f, bo, magic, cmd, cmddat, offset)?;
    }
    Ok(f)
}

fn load_command(r: &dyn ReadAt, f: &mut File, bo: Endian, magic: u32, cmd: u32, cmddat: &[u8], offset: u64) -> Result<(), String> {
    match cmd {
        LC_RPATH => {
            let h = Cursor::new(cmddat).take(12)?;
            let path = bo.u32(&h[8..]);
            if path as usize >= cmddat.len() {
                return Err(format_error(offset, "invalid path in rpath command", Some(path.to_string())));
            }
            f.loads.push(Load::Other);
        }
        LC_DYLIB => {
            let h = Cursor::new(cmddat).take(24)?;
            let name = bo.u32(&h[8..]);
            if name as usize >= cmddat.len() {
                return Err(format_error(offset, "invalid name in dynamic library command", Some(name.to_string())));
            }
            f.loads.push(Load::Dylib(cstring(&cmddat[name as usize..])));
        }
        LC_SYMTAB => {
            let h = Cursor::new(cmddat).take(24)?;
            let (symoff, nsyms, stroff, strsize) = (bo.u32(&h[8..]), bo.u32(&h[12..]), bo.u32(&h[16..]), bo.u32(&h[20..]));
            let strtab = read_data_at(r, u64::from(strsize), u64::from(stroff))?;
            let symsz: u64 = if magic == MAGIC64 { 16 } else { 12 };
            let symdat = read_data_at(r, u64::from(nsyms) * symsz, u64::from(symoff))?;
            f.symtab = Some(parse_symtab(&symdat, &strtab, bo, symsz as usize, nsyms, offset)?);
            f.loads.push(Load::Other);
        }
        LC_DYSYMTAB => {
            let h = Cursor::new(cmddat).take(80)?;
            let (iundef, nundef) = (bo.u32(&h[24..]), bo.u32(&h[28..]));
            let (indoff, nind) = (bo.u32(&h[56..]), bo.u32(&h[60..]));
            let Some(syms) = &f.symtab else {
                return Err(format_error(offset, "dynamic symbol table seen before any ordinary symbol table", None));
            };
            let len = syms.len();
            if iundef as usize > len {
                return Err(format_error(
                    offset,
                    &format!(
                        "undefined symbols index in dynamic symbol table command is greater than symbol table length ({iundef} > {len})"
                    ),
                    None,
                ));
            }
            let end = iundef.wrapping_add(nundef);
            if end as usize > len {
                return Err(format_error(
                    offset,
                    &format!(
                        "number of undefined symbols after index in dynamic symbol table command is greater than symbol table length ({end} > {len})"
                    ),
                    None,
                ));
            }
            read_data_at(r, u64::from(nind) * 4, u64::from(indoff))?;
            f.loads.push(Load::Other);
        }
        LC_SEGMENT | LC_SEGMENT64 => segment(r, f, bo, cmd == LC_SEGMENT64, cmddat, offset)?,
        _ => f.loads.push(Load::Other),
    }
    Ok(())
}

fn segment(r: &dyn ReadAt, f: &mut File, bo: Endian, wide: bool, cmddat: &[u8], offset: u64) -> Result<(), String> {
    let mut c = Cursor::new(cmddat);
    let seg = if wide {
        let h = c.take(72)?;
        let s = Segment {
            name: cstring(&h[8..24]),
            addr: bo.u64(&h[24..]),
            memsz: bo.u64(&h[32..]),
            offset: bo.u64(&h[40..]),
            filesz: bo.u64(&h[48..]),
            maxprot: bo.u32(&h[56..]),
            prot: bo.u32(&h[60..]),
        };
        (s, bo.u32(&h[64..]))
    } else {
        let h = c.take(56)?;
        let s = Segment {
            name: cstring(&h[8..24]),
            addr: u64::from(bo.u32(&h[24..])),
            memsz: u64::from(bo.u32(&h[28..])),
            offset: u64::from(bo.u32(&h[32..])),
            filesz: u64::from(bo.u32(&h[36..])),
            maxprot: bo.u32(&h[40..]),
            prot: bo.u32(&h[44..]),
        };
        (s, bo.u32(&h[48..]))
    };
    let (seg, nsect) = seg;
    let (seg_offset, seg_filesz) = (seg.offset, seg.filesz);
    f.loads.push(Load::Segment(seg));
    for _ in 0..nsect {
        let (s, reloff, nreloc) = if wide {
            let h = c.take(80)?;
            let s = Section {
                name: cstring(&h[0..16]),
                seg: cstring(&h[16..32]),
                addr: bo.u64(&h[32..]),
                size: bo.u64(&h[40..]),
                offset: bo.u32(&h[48..]),
            };
            (s, bo.u32(&h[56..]), bo.u32(&h[60..]))
        } else {
            let h = c.take(68)?;
            let s = Section {
                name: cstring(&h[0..16]),
                seg: cstring(&h[16..32]),
                addr: u64::from(bo.u32(&h[32..])),
                size: u64::from(bo.u32(&h[36..])),
                offset: bo.u32(&h[40..]),
            };
            (s, bo.u32(&h[48..]), bo.u32(&h[52..]))
        };
        f.sections.push(s);
        if nreloc > 0 {
            read_data_at(r, u64::from(nreloc) * 8, u64::from(reloff))?;
        }
    }
    if i64::try_from(seg_offset).is_err() {
        return Err(format_error(offset, "invalid section offset", Some(seg_offset.to_string())));
    }
    if i64::try_from(seg_filesz).is_err() {
        return Err(format_error(offset, "invalid section file size", Some(seg_filesz.to_string())));
    }
    Ok(())
}

fn parse_symtab(symdat: &[u8], strtab: &[u8], bo: Endian, symsz: usize, nsyms: u32, offset: u64) -> Result<Vec<Symbol>, String> {
    let mut c = Cursor::new(symdat);
    let mut out = Vec::with_capacity((nsyms as usize).min(1 << 20));
    for _ in 0..nsyms {
        let n = c.take(symsz)?;
        let (name, typ) = (bo.u32(&n[0..]), n[4]);
        if name as usize >= strtab.len() {
            return Err(format_error(offset, "invalid name in symbol table", Some(name.to_string())));
        }
        let mut s = cstring(&strtab[name as usize..]);
        // Go adds "_" to Go symbols and strips it again here (issue 33808).
        if s.contains('.') && s.starts_with('_') {
            s.remove(0);
        }
        out.push(Symbol { name: s, typ });
    }
    Ok(out)
}
