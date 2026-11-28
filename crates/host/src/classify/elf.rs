//! The parts of Go's `debug/elf` the classifier uses: `NewFile`'s validation, sections by name
//! and type, program headers, `Symbols`/`DynamicSymbols` names and `ImportedLibraries`
//! (`DT_NEEDED`). Compressed (`SHF_COMPRESSED`) section contents are not decompressed: reading
//! one fails, which Go only reaches for zlib/zstd-compressed `.rodata`, symbol or dynamic
//! sections.

use super::reader::{Endian, ReadAt, Section as SectionReader, read_data_at, read_full_at};

pub const SHT_STRTAB: u32 = 3;
pub const SHT_SYMTAB: u32 = 2;
pub const SHT_DYNAMIC: u32 = 6;
pub const SHT_NOBITS: u32 = 8;
pub const SHT_DYNSYM: u32 = 11;
const SHF_COMPRESSED: u64 = 0x800;
pub const PT_LOAD: u32 = 1;
pub const PF_X: u32 = 1;
pub const PF_W: u32 = 2;

pub struct Section {
    pub name: String,
    pub typ: u32,
    pub flags: u64,
    pub addr: u64,
    pub offset: u64,
    /// The (uncompressed) size, Go's `Section.Size`.
    pub size: u64,
    pub file_size: u64,
    pub link: u32,
}

pub struct Prog {
    pub typ: u32,
    pub flags: u32,
    pub off: u64,
    pub vaddr: u64,
    pub filesz: u64,
    pub memsz: u64,
}

pub struct File {
    pub wide: bool,
    pub bo: Endian,
    pub sections: Vec<Section>,
    pub progs: Vec<Prog>,
}

/// A symbol's name and `st_info`.
pub struct Symbol {
    pub name: String,
    pub info: u8,
}

fn invalid(msg: &str) -> String {
    format!("elf: {msg}")
}

/// Go's `elf.NewFile(r)`. Error texts are not Go's (they never reach output: a file that is not
/// ELF is reported as "neither Mach-O nor ELF").
pub fn parse(r: &dyn ReadAt) -> Result<File, String> {
    let mut ident = [0u8; 16];
    read_full_at(r, 0, &mut ident)?;
    if &ident[..4] != b"\x7fELF" {
        return Err(invalid("bad magic number"));
    }
    let wide = match ident[4] {
        1 => false,
        2 => true,
        _ => return Err(invalid("unknown ELF class")),
    };
    let bo = match ident[5] {
        1 => Endian::Little,
        2 => Endian::Big,
        _ => return Err(invalid("unknown ELF data encoding")),
    };
    if ident[6] != 1 {
        return Err(invalid("unknown ELF version"));
    }
    let mut hdr = vec![0u8; if wide { 64 } else { 52 }];
    read_full_at(r, 0, &mut hdr)?;
    if bo.u32(&hdr[20..]) != 1 {
        return Err(invalid("mismatched ELF version"));
    }
    let (phoff, phentsize, phnum, shoff, shentsize, mut shnum, mut shstrndx) = if wide {
        (
            bo.u64(&hdr[32..]),
            u64::from(bo.u16(&hdr[54..])),
            u64::from(bo.u16(&hdr[56..])),
            bo.u64(&hdr[40..]),
            u64::from(bo.u16(&hdr[58..])),
            u64::from(bo.u16(&hdr[60..])),
            u64::from(bo.u16(&hdr[62..])),
        )
    } else {
        (
            u64::from(bo.u32(&hdr[28..])),
            u64::from(bo.u16(&hdr[42..])),
            u64::from(bo.u16(&hdr[44..])),
            u64::from(bo.u32(&hdr[32..])),
            u64::from(bo.u16(&hdr[46..])),
            u64::from(bo.u16(&hdr[48..])),
            u64::from(bo.u16(&hdr[50..])),
        )
    };
    if i64::try_from(shoff).is_err() || i64::try_from(phoff).is_err() {
        return Err(invalid("invalid shoff/phoff"));
    }
    if shoff == 0 && shnum != 0 {
        return Err(invalid("invalid ELF shnum for shoff=0"));
    }
    if shnum > 0 && shstrndx >= shnum {
        return Err(invalid("invalid ELF shstrndx"));
    }
    let (want_ph, want_sh) = if wide { (56, 64) } else { (32, 40) };
    if phnum > 0 && phentsize < want_ph {
        return Err(invalid("invalid ELF phentsize"));
    }
    let phdata = read_data_at(r, phnum * phentsize, phoff)?;
    let mut progs = Vec::new();
    for i in 0..phnum {
        let p = &phdata[(i * phentsize) as usize..];
        let prog = if wide {
            Prog {
                typ: bo.u32(&p[0..]),
                flags: bo.u32(&p[4..]),
                off: bo.u64(&p[8..]),
                vaddr: bo.u64(&p[16..]),
                filesz: bo.u64(&p[32..]),
                memsz: bo.u64(&p[40..]),
            }
        } else {
            Prog {
                typ: bo.u32(&p[0..]),
                off: u64::from(bo.u32(&p[4..])),
                vaddr: u64::from(bo.u32(&p[8..])),
                filesz: u64::from(bo.u32(&p[16..])),
                memsz: u64::from(bo.u32(&p[20..])),
                flags: bo.u32(&p[24..]),
            }
        };
        if i64::try_from(prog.off).is_err() || i64::try_from(prog.filesz).is_err() {
            return Err(invalid("invalid program header"));
        }
        progs.push(prog);
    }
    if shoff > 0 && shnum == 0 {
        // Extended section numbering: the real count is in section 0's sh_size.
        let mut sh = vec![0u8; want_sh as usize];
        read_full_at(r, shoff, &mut sh)?;
        let (typ, size, link) = if wide {
            (bo.u32(&sh[4..]), bo.u64(&sh[32..]), bo.u32(&sh[40..]))
        } else {
            (bo.u32(&sh[4..]), u64::from(bo.u32(&sh[20..])), bo.u32(&sh[24..]))
        };
        if typ != 0 {
            return Err(invalid("invalid type of the initial section"));
        }
        shnum = size;
        if shnum < 0xff00 {
            return Err(invalid("invalid ELF shnum contained in sh_size"));
        }
        if shstrndx == 0xffff {
            shstrndx = u64::from(link);
            if shstrndx < 0xff00 {
                return Err(invalid("invalid ELF shstrndx contained in sh_link"));
            }
        }
    }
    if shnum > 0 && shentsize < want_sh {
        return Err(invalid("invalid ELF shentsize"));
    }
    if shnum > 0 && u64::MAX / shnum < shentsize {
        return Err(invalid("section header overflow"));
    }
    let shdata = read_data_at(r, shnum * shentsize, shoff)?;
    let mut sections = Vec::new();
    let mut names = Vec::new();
    for i in 0..shnum {
        let s = &shdata[(i * shentsize) as usize..];
        let (name, mut sec) = if wide {
            (
                bo.u32(&s[0..]),
                Section {
                    name: String::new(),
                    typ: bo.u32(&s[4..]),
                    flags: bo.u64(&s[8..]),
                    addr: bo.u64(&s[16..]),
                    offset: bo.u64(&s[24..]),
                    file_size: bo.u64(&s[32..]),
                    size: 0,
                    link: bo.u32(&s[40..]),
                },
            )
        } else {
            (
                bo.u32(&s[0..]),
                Section {
                    name: String::new(),
                    typ: bo.u32(&s[4..]),
                    flags: u64::from(bo.u32(&s[8..])),
                    addr: u64::from(bo.u32(&s[12..])),
                    offset: u64::from(bo.u32(&s[16..])),
                    file_size: u64::from(bo.u32(&s[20..])),
                    size: 0,
                    link: bo.u32(&s[24..]),
                },
            )
        };
        if i64::try_from(sec.offset).is_err() || i64::try_from(sec.file_size).is_err() {
            return Err(invalid("invalid section offset or size"));
        }
        sec.size = sec.file_size;
        if sec.flags & SHF_COMPRESSED != 0 {
            let reader = SectionReader { r, off: sec.offset, size: sec.file_size };
            let mut ch = vec![0u8; if wide { 24 } else { 12 }];
            read_full_at(&reader, 0, &mut ch)?;
            sec.size = if wide { bo.u64(&ch[8..]) } else { u64::from(bo.u32(&ch[4..])) };
        }
        names.push(name);
        sections.push(sec);
    }
    let mut f = File { wide, bo, sections, progs };
    if f.sections.is_empty() || shstrndx == 0 {
        return Ok(f);
    }
    let shstr = &f.sections[shstrndx as usize];
    if shstr.typ != SHT_STRTAB {
        return Err(invalid("invalid ELF section name string table type"));
    }
    let shstrtab = f.data(r, shstrndx as usize)?;
    for (i, s) in f.sections.iter_mut().enumerate() {
        s.name = get_string(&shstrtab, names[i] as usize).ok_or_else(|| invalid("bad section name index"))?;
    }
    Ok(f)
}

/// Go's `getString`: the NUL-terminated string at `start`.
fn get_string(section: &[u8], start: usize) -> Option<String> {
    let rest = section.get(start..)?;
    let end = rest.iter().position(|&c| c == 0)?;
    Some(String::from_utf8_lossy(&rest[..end]).into_owned())
}

impl File {
    /// Go's `File.Section(name)`: the first section so named.
    pub fn section(&self, name: &str) -> Option<&Section> {
        self.sections.iter().find(|s| s.name == name)
    }

    fn section_index_by_type(&self, typ: u32) -> Option<usize> {
        self.sections.iter().position(|s| s.typ == typ)
    }

    /// A reader over `s`'s bytes (Go's `Section.Open` for an uncompressed section).
    pub fn section_reader<'a>(r: &'a dyn ReadAt, s: &Section) -> SectionReader<'a> {
        SectionReader { r, off: s.offset, size: s.file_size }
    }

    /// Go's `Section.Data()`.
    pub fn data(&self, r: &dyn ReadAt, i: usize) -> Result<Vec<u8>, String> {
        let s = &self.sections[i];
        if s.typ == SHT_NOBITS {
            return if s.size < 1 << 30 { Ok(vec![0; s.size as usize]) } else { Err(super::reader::UNEXPECTED_EOF.to_string()) };
        }
        if s.flags & SHF_COMPRESSED != 0 {
            return Err(invalid("compressed section"));
        }
        read_data_at(&Self::section_reader(r, s), s.size, 0)
    }

    /// Go's `stringTable(link)`.
    fn string_table(&self, r: &dyn ReadAt, link: u32) -> Result<Vec<u8>, String> {
        if link == 0 || link as usize >= self.sections.len() {
            return Err(invalid("section has invalid string table link"));
        }
        self.data(r, link as usize)
    }

    /// Go's `Symbols()` (`SHT_SYMTAB`) or `DynamicSymbols()` (`SHT_DYNSYM`): names and info.
    pub fn symbols(&self, r: &dyn ReadAt, typ: u32) -> Result<Vec<Symbol>, String> {
        let i = self.section_index_by_type(typ).ok_or_else(|| invalid("no symbol section"))?;
        let data = self.data(r, i)?;
        if data.is_empty() {
            return Err(invalid("symbol section is empty"));
        }
        let size = if self.wide { 24 } else { 16 };
        if data.len() % size != 0 {
            return Err(invalid("length of symbol section is not a multiple of SymSize"));
        }
        let strdata = self.string_table(r, self.sections[i].link)?;
        let info_at = if self.wide { 4 } else { 12 };
        Ok(data[size..]
            .chunks_exact(size)
            .map(|s| Symbol { name: get_string(&strdata, self.bo.u32(s) as usize).unwrap_or_default(), info: s[info_at] })
            .collect())
    }

    /// Go's `ImportedLibraries()`: the `DT_NEEDED` strings of the dynamic section.
    pub fn imported_libraries(&self, r: &dyn ReadAt) -> Result<Vec<String>, String> {
        let Some(i) = self.section_index_by_type(SHT_DYNAMIC) else { return Ok(Vec::new()) };
        let d = self.data(r, i)?;
        let size = if self.wide { 16 } else { 8 };
        if d.len() % size != 0 {
            return Err(invalid("length of dynamic section is not a multiple of dynamic entry size"));
        }
        let strtab = self.string_table(r, self.sections[i].link)?;
        let mut out = Vec::new();
        for e in d.chunks_exact(size) {
            let (tag, val) = if self.wide {
                (self.bo.u64(e) as i64, self.bo.u64(&e[8..]))
            } else {
                (i64::from(self.bo.u32(e) as i32), u64::from(self.bo.u32(&e[4..])))
            };
            if tag == 1
                && let Some(s) = usize::try_from(val).ok().and_then(|v| get_string(&strtab, v))
            {
                out.push(s);
            }
        }
        Ok(out)
    }
}
