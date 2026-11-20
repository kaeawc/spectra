package classify

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/kaeawc/spectra/internal/proc"
)

// sect is one section of a synthetic binary.
type sect struct {
	seg, name string
	data      []byte
}

// buildMachO returns a minimal 64-bit Mach-O executable whose sections hold
// the given data. Each distinct segment gets one LC_SEGMENT_64.
func buildMachO(cpu macho.Cpu, sects ...sect) []byte {
	var segs []string
	bySeg := map[string][]sect{}
	for _, s := range sects {
		if _, ok := bySeg[s.seg]; !ok {
			segs = append(segs, s.seg)
		}
		bySeg[s.seg] = append(bySeg[s.seg], s)
	}
	const hdr, segCmd, sectHdr = 32, 72, 80
	cmdsSize := 0
	for _, seg := range segs {
		cmdsSize += segCmd + sectHdr*len(bySeg[seg])
	}
	dataOff := hdr + cmdsSize
	le := binary.LittleEndian
	var cmds, data bytes.Buffer
	for _, seg := range segs {
		ss := bySeg[seg]
		segStart := dataOff + data.Len()
		segSize := 0
		for _, s := range ss {
			segSize += len(s.data)
		}
		_ = binary.Write(&cmds, le, uint32(macho.LoadCmdSegment64))
		_ = binary.Write(&cmds, le, uint32(segCmd+sectHdr*len(ss)))
		cmds.Write(name16(seg))
		_ = binary.Write(&cmds, le, []uint64{0x1000, uint64(segSize), uint64(segStart), uint64(segSize)})
		_ = binary.Write(&cmds, le, []uint32{7, 5, uint32(len(ss)), 0})
		for _, s := range ss {
			off := dataOff + data.Len()
			cmds.Write(name16(s.name))
			cmds.Write(name16(seg))
			_ = binary.Write(&cmds, le, []uint64{0x1000, uint64(len(s.data))})
			_ = binary.Write(&cmds, le, []uint32{uint32(off), 0, 0, 0, 0, 0, 0, 0})
			data.Write(s.data)
		}
	}
	var out bytes.Buffer
	_ = binary.Write(&out, le, []uint32{macho.Magic64, uint32(cpu), 0, uint32(macho.TypeExec), uint32(len(segs)), uint32(cmdsSize), 0, 0})
	out.Write(cmds.Bytes())
	out.Write(data.Bytes())
	return out.Bytes()
}

func name16(s string) []byte {
	b := make([]byte, 16)
	copy(b, s)
	return b
}

// buildFat wraps thin Mach-O slices in a universal binary.
func buildFat(slices map[macho.Cpu][]byte, order ...macho.Cpu) []byte {
	be := binary.BigEndian
	var out bytes.Buffer
	_ = binary.Write(&out, be, []uint32{macho.MagicFat, uint32(len(order))})
	off := 8 + 20*len(order)
	off = (off + 15) &^ 15
	offsets := make([]int, len(order))
	cur := off
	for i, cpu := range order {
		offsets[i] = cur
		cur += (len(slices[cpu]) + 15) &^ 15
	}
	for i, cpu := range order {
		_ = binary.Write(&out, be, []uint32{uint32(cpu), 0, uint32(offsets[i]), uint32(len(slices[cpu])), 4})
	}
	for i, cpu := range order {
		for out.Len() < offsets[i] {
			out.WriteByte(0)
		}
		out.Write(slices[cpu])
	}
	return out.Bytes()
}

// buildELF returns a minimal 64-bit little-endian ELF with the named
// PROGBITS sections.
func buildELF(sects ...sect) []byte {
	le := binary.LittleEndian
	shstr := []byte{0}
	nameOff := make([]int, len(sects))
	for i, s := range sects {
		nameOff[i] = len(shstr)
		shstr = append(shstr, s.name...)
		shstr = append(shstr, 0)
	}
	shstrName := len(shstr)
	shstr = append(shstr, ".shstrtab\x00"...)

	var data bytes.Buffer
	offs := make([]int, len(sects))
	for i, s := range sects {
		offs[i] = 64 + data.Len()
		data.Write(s.data)
	}
	shstrOff := 64 + data.Len()
	data.Write(shstr)
	for data.Len()%8 != 0 {
		data.WriteByte(0)
	}
	shoff := 64 + data.Len()
	shnum := len(sects) + 2

	var out bytes.Buffer
	out.Write([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	_ = binary.Write(&out, le, []uint16{2, 62})
	_ = binary.Write(&out, le, uint32(1))
	_ = binary.Write(&out, le, []uint64{0, 0, uint64(shoff)})
	_ = binary.Write(&out, le, uint32(0))
	_ = binary.Write(&out, le, []uint16{64, 56, 0, 64, uint16(shnum), uint16(shnum - 1)})
	out.Write(data.Bytes())
	shdr := func(name, typ uint32, off, size int) {
		_ = binary.Write(&out, le, []uint32{name, typ})
		_ = binary.Write(&out, le, []uint64{2, 0, uint64(off), uint64(size)})
		_ = binary.Write(&out, le, []uint32{0, 0})
		_ = binary.Write(&out, le, []uint64{1, 0})
	}
	out.Write(make([]byte, 64)) // SHN_UNDEF
	for i, s := range sects {
		shdr(uint32(nameOff[i]), 1, offs[i], len(s.data))
	}
	shdr(uint32(shstrName), 3, shstrOff, len(shstr))
	return out.Bytes()
}

// fakeFS serves an fstest.MapFS under absolute paths and can deny reads.
type fakeFS struct {
	files fstest.MapFS
	deny  map[string]bool
	links map[string]string // EvalSymlinks results
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: fstest.MapFS{}, deny: map[string]bool{}, links: map[string]string{}}
}

var fixtureTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func (f *fakeFS) add(path string, data []byte) {
	f.files[strings.TrimPrefix(path, "/")] = &fstest.MapFile{Data: data, Mode: 0o755, ModTime: fixtureTime}
}

func (f *fakeFS) Stat(path string) (fs.FileInfo, error) {
	return fs.Stat(f.files, strings.TrimPrefix(path, "/"))
}

func (f *fakeFS) Open(path string) (ReadAtCloser, error) {
	if f.deny[path] {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	file, err := f.files.Open(strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, err
	}
	rac, ok := file.(ReadAtCloser)
	if !ok {
		file.Close()
		return nil, errors.New("not a regular file")
	}
	return rac, nil
}

func (f *fakeFS) ReadFile(path string, limit int64) ([]byte, error) {
	if f.deny[path] {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	data, err := fs.ReadFile(f.files, strings.TrimPrefix(path, "/"))
	if err == nil && int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, err
}

func (f *fakeFS) EvalSymlinks(path string) (string, error) {
	if to, ok := f.links[path]; ok {
		return to, nil
	}
	return path, nil
}

// memStore is an in-memory Store that counts traffic.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
	gets int
	puts int
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (m *memStore) Get(key []byte) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	d, ok := m.data[string(key)]
	return d, ok, nil
}

func (m *memStore) Put(key, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.data[string(key)] = append([]byte(nil), data...)
	return nil
}

func infoPlist(id, exe string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + id + `</string>
<key>CFBundleExecutable</key><string>` + exe + `</string>
</dict></plist>`)
}

// codesignFake answers every codesign call with out on stderr.
func codesignFake(out string) *proc.Fake {
	return proc.NewFake().On(proc.MatchPrefix("codesign"), proc.Response{Result: proc.Result{Stderr: []byte(out), ExitCode: 0}})
}

// elfSym is one function symbol of a synthetic ELF symbol table.
type elfSym struct {
	name  string
	local bool // STB_LOCAL instead of STB_GLOBAL
}

// buildELFWithSymtab returns a minimal 64-bit little-endian ELF with a
// .rodata section and an unstripped .symtab of function symbols. Locals
// must come first, as ELF requires.
func buildELFWithSymtab(rodata []byte, syms ...elfSym) []byte {
	le := binary.LittleEndian
	strtab := []byte{0}
	var symtab bytes.Buffer
	symtab.Write(make([]byte, 24)) // the null symbol
	firstGlobal := 1
	for i, s := range syms {
		info := byte(0x12) // STB_GLOBAL, STT_FUNC
		if s.local {
			info = 0x02 // STB_LOCAL, STT_FUNC
			firstGlobal = i + 2
		}
		_ = binary.Write(&symtab, le, uint32(len(strtab)))
		symtab.Write([]byte{info, 0})
		_ = binary.Write(&symtab, le, uint16(1)) // defined in .rodata's slot; only the name matters
		_ = binary.Write(&symtab, le, []uint64{0x1000, 16})
		strtab = append(strtab, s.name...)
		strtab = append(strtab, 0)
	}
	shstr := []byte("\x00.rodata\x00.symtab\x00.strtab\x00.shstrtab\x00")
	const nRodata, nSymtab, nStrtab, nShstr = 1, 9, 17, 25

	var data bytes.Buffer
	pad := func() {
		for data.Len()%8 != 0 {
			data.WriteByte(0)
		}
	}
	rodataOff := 64 + data.Len()
	data.Write(rodata)
	pad()
	symOff := 64 + data.Len()
	data.Write(symtab.Bytes())
	strOff := 64 + data.Len()
	data.Write(strtab)
	shstrOff := 64 + data.Len()
	data.Write(shstr)
	pad()
	shoff := 64 + data.Len()
	const shnum = 5

	var out bytes.Buffer
	out.Write([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	_ = binary.Write(&out, le, []uint16{2, 62})
	_ = binary.Write(&out, le, uint32(1))
	_ = binary.Write(&out, le, []uint64{0, 0, uint64(shoff)})
	_ = binary.Write(&out, le, uint32(0))
	_ = binary.Write(&out, le, []uint16{64, 56, 0, 64, shnum, shnum - 1})
	out.Write(data.Bytes())
	shdr := func(name, typ uint32, off, size int, link, info uint32, entsize uint64) {
		_ = binary.Write(&out, le, []uint32{name, typ})
		_ = binary.Write(&out, le, []uint64{2, 0, uint64(off), uint64(size)})
		_ = binary.Write(&out, le, []uint32{link, info})
		_ = binary.Write(&out, le, []uint64{8, entsize})
	}
	out.Write(make([]byte, 64)) // SHN_UNDEF
	shdr(nRodata, 1, rodataOff, len(rodata), 0, 0, 0)
	shdr(nSymtab, 2, symOff, symtab.Len(), 3, uint32(firstGlobal), 24)
	shdr(nStrtab, 3, strOff, len(strtab), 0, 0, 0)
	shdr(nShstr, 3, shstrOff, len(shstr), 0, 0, 0)
	return out.Bytes()
}
