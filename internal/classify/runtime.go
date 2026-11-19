package classify

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	goruntime "runtime"
	"strings"
)

// Runtime is the language runtime a process executes on.
type Runtime string

const (
	RuntimeElectron Runtime = "electron"
	RuntimeNative   Runtime = "native"
	RuntimeRust     Runtime = "rust"
	RuntimeGo       Runtime = "go"
	RuntimeJVM      Runtime = "jvm"
	RuntimeNode     Runtime = "node"
	RuntimeBun      Runtime = "bun"
	RuntimeDeno     Runtime = "deno"
	RuntimePython   Runtime = "python"
	RuntimeRuby     Runtime = "ruby"
	RuntimeShell    Runtime = "shell"
	RuntimeUnknown  Runtime = "unknown"
)

// DefaultMaxScanBytes caps how much of one binary the Rust marker scan reads.
const DefaultMaxScanBytes int64 = 32 << 20

var (
	pythonName = regexp.MustCompile(`^python[0-9.]*$`)
	rubyName   = regexp.MustCompile(`^ruby[0-9.]*$`)
)

// shellNames are interactive/login shells, matched on executable base name.
var shellNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"mksh": true, "tcsh": true, "csh": true, "nu": true, "pwsh": true, "elvish": true,
}

// interpreterRuntime identifies interpreters and managed runtimes by
// executable base name (or argv[0] when the executable path is not visible).
func interpreterRuntime(base string) (Runtime, bool) {
	name := strings.ToLower(strings.TrimPrefix(base, "-"))
	switch {
	case name == "java":
		return RuntimeJVM, true
	case name == "node" || name == "nodejs":
		return RuntimeNode, true
	case name == "bun":
		return RuntimeBun, true
	case name == "deno":
		return RuntimeDeno, true
	case pythonName.MatchString(name):
		return RuntimePython, true
	case rubyName.MatchString(name):
		return RuntimeRuby, true
	case shellNames[name]:
		return RuntimeShell, true
	}
	return "", false
}

// binaryVerdict is the result of inspecting one executable file.
type binaryVerdict struct {
	Runtime  Runtime `json:"runtime"`
	Evidence string  `json:"evidence"`
	// GoModule is the main module path from Go build info, when present.
	GoModule string `json:"go_module,omitempty"`
}

// rustNeedles are strings rustc's standard library leaves in read-only data
// even in stripped release builds: panic locations under /rustc/<hash>/ and
// the RUST_BACKTRACE variable std reads on panic. They prove Rust std is
// linked, not that the program is Rust: C++ programs that link a Rust
// library (adb, netsimd) carry them too.
var rustNeedles = [][]byte{[]byte("/rustc/"), []byte("RUST_BACKTRACE")}

// rustEntrySymbol is std::rt::lang_start, which only a Rust main calls. It
// appears as "...3std2rt10lang_start..." in both legacy (_ZN) and v0 (_R)
// mangling; the length prefix keeps lang_start_internal (19...) out.
const rustEntrySymbol = "3std2rt10lang_start"

func inspectBinary(files FileSystem, path string, maxScan int64) binaryVerdict {
	f, err := files.Open(path)
	if err != nil {
		return binaryVerdict{Runtime: RuntimeUnknown, Evidence: fmt.Sprintf("executable unreadable: %v", err)}
	}
	defer f.Close()

	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		return binaryVerdict{Runtime: RuntimeUnknown, Evidence: fmt.Sprintf("executable unreadable: %v", err)}
	}
	if binary.BigEndian.Uint32(magic[:]) == macho.MagicFat {
		fat, err := macho.NewFatFile(f)
		if err != nil {
			return binaryVerdict{Runtime: RuntimeUnknown, Evidence: fmt.Sprintf("malformed universal binary: %v", err)}
		}
		slice := pickFatSlice(fat)
		v := machoVerdict(slice.File, maxScan)
		if v.Runtime == RuntimeGo {
			v.GoModule = goModule(io.NewSectionReader(f, int64(slice.Offset), int64(slice.Size)))
		}
		v.Evidence += fmt.Sprintf(" (universal binary, %s slice)", slice.Cpu)
		return v
	}
	var v binaryVerdict
	if mf, err := macho.NewFile(f); err == nil {
		v = machoVerdict(mf, maxScan)
	} else if ef, err := elf.NewFile(f); err == nil {
		v = elfVerdict(ef, maxScan)
	} else {
		return binaryVerdict{Runtime: RuntimeUnknown, Evidence: "executable is neither Mach-O nor ELF"}
	}
	if v.Runtime == RuntimeGo {
		v.GoModule = goModule(f)
	}
	return v
}

func goModule(r io.ReaderAt) string {
	bi, err := buildinfo.Read(r)
	if err != nil {
		return ""
	}
	if bi.Main.Path != "" {
		return bi.Main.Path
	}
	return bi.Path
}

// pickFatSlice prefers the host architecture's slice: that is the code the
// process actually runs.
func pickFatSlice(fat *macho.FatFile) macho.FatArch {
	want := macho.CpuArm64
	if goruntime.GOARCH == "amd64" {
		want = macho.CpuAmd64
	}
	for _, a := range fat.Arches {
		if a.Cpu == want {
			return a
		}
	}
	return fat.Arches[0]
}

func machoVerdict(f *macho.File, maxScan int64) binaryVerdict {
	if f.Segment("__BUN") != nil {
		return binaryVerdict{Runtime: RuntimeBun, Evidence: "Mach-O has a __BUN segment (bun compiled executable)"}
	}
	for _, s := range f.Sections {
		if s.Name == "__go_buildinfo" {
			return binaryVerdict{Runtime: RuntimeGo, Evidence: "Mach-O has a __go_buildinfo section"}
		}
	}
	budget := maxScan
	hits := 0
	for _, s := range f.Sections {
		if !machoReadOnlyData(s.Seg, s.Name) || budget <= 0 {
			continue
		}
		hits += countMarkers(s.Open(), &budget)
	}
	libs, _ := f.ImportedLibraries()
	return rustVerdict("Mach-O", hits, machoSymbols(f), libs)
}

// symbolFacts is what the Rust decision needs from a symbol table.
type symbolFacts struct {
	rustEntry bool // std::rt::lang_start is defined
	locals    bool // local function symbols survive (not stripped)
	cxx       bool // C++ runtime symbols (libc++/libstdc++, operator new, EH)
}

func (s *symbolFacts) add(name string) {
	if strings.Contains(name, rustEntrySymbol) {
		s.rustEntry = true
	}
	if cxxSymbol(name) {
		s.cxx = true
	}
}

func cxxSymbol(name string) bool {
	n := strings.TrimLeft(name, "_")
	switch n {
	case "Znwm", "Znwj", "cxa_throw", "cxa_begin_catch", "gxx_personality_v0":
		return true
	}
	return strings.HasPrefix(n, "ZNSt") || strings.HasPrefix(n, "ZNKSt")
}

func machoSymbols(f *macho.File) symbolFacts {
	var s symbolFacts
	if f.Symtab == nil {
		return s
	}
	const nStab, nType, nExt, nSect = 0xe0, 0x0e, 0x01, 0x0e
	for _, sym := range f.Symtab.Syms {
		s.add(sym.Name)
		if sym.Type&nStab == 0 && sym.Type&nType == nSect && sym.Type&nExt == 0 {
			s.locals = true
		}
	}
	return s
}

func elfSymbols(f *elf.File) symbolFacts {
	var s symbolFacts
	if syms, err := f.Symbols(); err == nil {
		for _, sym := range syms {
			s.add(sym.Name)
			if elf.ST_BIND(sym.Info) == elf.STB_LOCAL && elf.ST_TYPE(sym.Info) == elf.STT_FUNC {
				s.locals = true
			}
		}
	}
	if syms, err := f.DynamicSymbols(); err == nil {
		for _, sym := range syms {
			s.add(sym.Name)
		}
	}
	return s
}

// rustVerdict decides rust versus native once Rust std markers are counted.
// A Rust main always calls std::rt::lang_start, so with an unstripped symbol
// table its absence means Rust is only linked in. A stripped table loses that
// symbol; there a C++ runtime (always present when C++ owns main) marks the
// program as C++ that links Rust, and its absence leaves Rust as the owner.
func rustVerdict(format string, hits int, syms symbolFacts, libs []string) binaryVerdict {
	cxx := syms.cxx || linksCxx(libs)
	switch {
	case syms.rustEntry:
		return binaryVerdict{Runtime: RuntimeRust, Evidence: format + " defines std::rt::lang_start (Rust main)"}
	case hits == 0:
		return binaryVerdict{Runtime: RuntimeNative, Evidence: format + " with no Go, Bun or Rust markers"}
	case syms.locals:
		return binaryVerdict{Runtime: RuntimeNative, Evidence: fmt.Sprintf("links Rust std (%d rustc markers) but the symbol table has no std::rt::lang_start", hits)}
	case cxx:
		return binaryVerdict{Runtime: RuntimeNative, Evidence: fmt.Sprintf("links Rust std (%d rustc markers) into a stripped binary with a C++ runtime", hits)}
	}
	return binaryVerdict{Runtime: RuntimeRust, Evidence: fmt.Sprintf("stripped %s with %d rustc std markers and no C++ runtime", format, hits)}
}

func linksCxx(libs []string) bool {
	for _, l := range libs {
		if strings.Contains(l, "libc++") || strings.Contains(l, "libstdc++") {
			return true
		}
	}
	return false
}

func machoReadOnlyData(seg, name string) bool {
	switch seg {
	case "__TEXT":
		return name == "__const" || name == "__cstring" || name == "__rodata"
	case "__DATA_CONST":
		return name == "__const"
	}
	return false
}

func elfVerdict(f *elf.File, maxScan int64) binaryVerdict {
	if f.Section(".go.buildinfo") != nil {
		return binaryVerdict{Runtime: RuntimeGo, Evidence: "ELF has a .go.buildinfo section"}
	}
	if f.Section(".bun") != nil {
		return binaryVerdict{Runtime: RuntimeBun, Evidence: "ELF has a .bun section (bun compiled executable)"}
	}
	budget := maxScan
	hits := 0
	if s := f.Section(".rodata"); s != nil && s.Type != elf.SHT_NOBITS {
		hits = countMarkers(s.Open(), &budget)
	}
	libs, _ := f.ImportedLibraries()
	return rustVerdict("ELF", hits, elfSymbols(f), libs)
}

// countMarkers counts rustNeedles in r, reading at most *budget bytes and
// charging what it read against the budget.
func countMarkers(r io.Reader, budget *int64) int {
	const chunk = 256 << 10
	overlap := 0
	for _, n := range rustNeedles {
		overlap = max(overlap, len(n)-1)
	}
	buf := make([]byte, overlap+chunk)
	carry := 0
	hits := 0
	for *budget > 0 {
		want := int64(chunk)
		if *budget < want {
			want = *budget
		}
		n, err := io.ReadFull(r, buf[carry:carry+int(want)])
		*budget -= int64(n)
		region := buf[:carry+n]
		for _, needle := range rustNeedles {
			// Matches wholly inside the carried tail were counted last round.
			hits += bytes.Count(region, needle) - bytes.Count(region[:carry], needle)
		}
		if err != nil {
			break
		}
		// Carry a tail so a match straddling two chunks is still found.
		carry = min(overlap, len(region))
		copy(buf, region[len(region)-carry:])
	}
	return hits
}
