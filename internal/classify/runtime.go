package classify

import (
	"bytes"
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
}

// rustNeedles are strings rustc's standard library leaves in read-only data
// even in stripped release builds: panic locations under /rustc/<hash>/ and
// the RUST_BACKTRACE variable std reads on panic.
var rustNeedles = [][]byte{[]byte("/rustc/"), []byte("RUST_BACKTRACE")}

func inspectBinary(files FileSystem, path string, maxScan int64) binaryVerdict {
	f, err := files.Open(path)
	if err != nil {
		return binaryVerdict{RuntimeUnknown, fmt.Sprintf("executable unreadable: %v", err)}
	}
	defer f.Close()

	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		return binaryVerdict{RuntimeUnknown, fmt.Sprintf("executable unreadable: %v", err)}
	}
	if binary.BigEndian.Uint32(magic[:]) == macho.MagicFat {
		fat, err := macho.NewFatFile(f)
		if err != nil {
			return binaryVerdict{RuntimeUnknown, fmt.Sprintf("malformed universal binary: %v", err)}
		}
		slice := pickFatSlice(fat)
		v := machoVerdict(slice.File, maxScan)
		v.Evidence += fmt.Sprintf(" (universal binary, %s slice)", slice.Cpu)
		return v
	}
	if mf, err := macho.NewFile(f); err == nil {
		return machoVerdict(mf, maxScan)
	}
	if ef, err := elf.NewFile(f); err == nil {
		return elfVerdict(ef, maxScan)
	}
	return binaryVerdict{RuntimeUnknown, "executable is neither Mach-O nor ELF"}
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
		return binaryVerdict{RuntimeBun, "Mach-O has a __BUN segment (bun compiled executable)"}
	}
	for _, s := range f.Sections {
		if s.Name == "__go_buildinfo" {
			return binaryVerdict{RuntimeGo, "Mach-O has a __go_buildinfo section"}
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
	if hits > 0 {
		return binaryVerdict{RuntimeRust, fmt.Sprintf("%d rustc std markers in Mach-O read-only data", hits)}
	}
	return binaryVerdict{RuntimeNative, "Mach-O with no Go, Bun or Rust markers"}
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
		return binaryVerdict{RuntimeGo, "ELF has a .go.buildinfo section"}
	}
	if f.Section(".bun") != nil {
		return binaryVerdict{RuntimeBun, "ELF has a .bun section (bun compiled executable)"}
	}
	budget := maxScan
	hits := 0
	if s := f.Section(".rodata"); s != nil && s.Type != elf.SHT_NOBITS {
		hits = countMarkers(s.Open(), &budget)
	}
	if hits > 0 {
		return binaryVerdict{RuntimeRust, fmt.Sprintf("%d rustc std markers in ELF .rodata", hits)}
	}
	return binaryVerdict{RuntimeNative, "ELF with no Go, Bun or Rust markers"}
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
