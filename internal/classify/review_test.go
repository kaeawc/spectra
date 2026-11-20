package classify

import (
	"bytes"
	"context"
	"debug/macho"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func TestTransientReadFailureIsNotCachedAndRetries(t *testing.T) {
	files := newFakeFS()
	files.add("/proc/9/exe", buildELF(sect{name: ".rodata", data: rustData()}))
	files.deny["/proc/9/exe"] = true
	store := newMemStore()
	c := New(Options{OS: hostos.Linux, FS: files, Store: store})
	in := Input{PID: 9, PPID: 1, UID: 501, Command: "tool", ExecutablePath: "/opt/tool", CommandLine: "/opt/tool"}

	if r := classifyOne(t, c, in); r.Runtime != RuntimeUnknown {
		t.Fatalf("first runtime = %s, want unknown while unreadable", r.Runtime)
	}
	if store.puts != 0 {
		t.Fatalf("unreadable verdict persisted to disk (%d entries)", store.puts)
	}
	delete(files.deny, "/proc/9/exe")
	if r := classifyOne(t, c, in); r.Runtime != RuntimeRust {
		t.Fatalf("second runtime = %s, want rust after the read recovers", r.Runtime)
	}
	if store.puts != 1 {
		t.Fatalf("completed inspection not persisted (%d entries)", store.puts)
	}
}

type flakyRunner struct {
	inner proc.Runner
	fail  atomic.Bool
}

func (r *flakyRunner) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	if r.fail.Load() {
		return proc.Result{}, context.DeadlineExceeded
	}
	return r.inner.Run(ctx, cmd)
}

func TestSigningFailureIsNotCachedAndRetries(t *testing.T) {
	files := newFakeFS()
	exe := "/opt/homebrew/bin/rg"
	files.add(exe, buildMachO(macho.CpuArm64, sect{"__TEXT", "__cstring", []byte("c\x00")}))
	runner := &flakyRunner{inner: codesignFake(adhocCodesign)}
	runner.fail.Store(true)
	store := newMemStore()
	c := New(Options{OS: hostos.Darwin, FS: files, Runner: runner, Store: store})
	in := Input{PID: 3, PPID: 1, UID: 501, Command: "rg", ExecutablePath: exe, CommandLine: exe}

	r := classifyOne(t, c, in)
	if r.Signing == nil || r.Signing.Status != SigningUnknown {
		t.Fatalf("first signing = %+v, want unknown", r.Signing)
	}
	if store.puts != 0 {
		t.Fatalf("failed signing persisted to disk")
	}
	runner.fail.Store(false)
	r = classifyOne(t, c, in)
	if r.Signing == nil || r.Signing.Status != SigningAdhoc {
		t.Fatalf("second signing = %+v, want adhoc after the runner recovers", r.Signing)
	}
}

func TestPoolDispatchesNothingAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	err := New(Options{Workers: 4}).pool(ctx, 50, func(int) { calls.Add(1) })
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d, want canceled and no jobs run", err, calls.Load())
	}
}

func TestInspectBinarySkipsOversizedSymbolTables(t *testing.T) {
	elf := buildELF(sect{name: ".rodata", data: rustData()}, sect{name: ".symtab", data: []byte("x")})
	shoff := binary.LittleEndian.Uint64(elf[40:])
	hdr := elf[shoff+64*2:]                   // section 2 is .symtab
	binary.LittleEndian.PutUint32(hdr[4:], 2) // SHT_SYMTAB
	binary.LittleEndian.PutUint64(hdr[32:], 1<<40)
	files := newFakeFS()
	files.add("/bin/huge", elf)
	v, err := inspectBinary(files, "/bin/huge", DefaultMaxScanBytes)
	if err != nil || v.Runtime != RuntimeRust || !strings.Contains(v.Evidence, "symbol table too large") {
		t.Fatalf("verdict = %+v, %v; want rust without reading the oversized table", v, err)
	}
	files.add("/bin/plain", buildELF(sect{name: ".rodata", data: []byte("hello")}))
	if v, _ := inspectBinary(files, "/bin/plain", DefaultMaxScanBytes); strings.Contains(v.Evidence, "symbol table") {
		t.Fatalf("unmarked binary consulted symbols: %+v", v)
	}
}

func symtabOnlyMachO(nsyms, strsize uint32) []byte {
	le := binary.LittleEndian
	var b bytes.Buffer
	_ = binary.Write(&b, le, []uint32{macho.Magic64, uint32(macho.CpuArm64), 0, uint32(macho.TypeExec), 1, 24, 0, 0})
	_ = binary.Write(&b, le, []uint32{uint32(macho.LoadCmdSymtab), 24, 4096, nsyms, 8192, strsize})
	return b.Bytes()
}

func TestBoundSymtabZeroesOversizedTable(t *testing.T) {
	huge := symtabOnlyMachO(1<<28, 1<<28)
	if _, err := macho.NewFile(bytes.NewReader(huge)); err == nil {
		t.Skip("debug/macho no longer loads the symbol table eagerly")
	}
	mr, skipped := boundSymtab(bytes.NewReader(huge), DefaultMaxScanBytes)
	if !skipped {
		t.Fatal("oversized symtab not flagged")
	}
	f, err := macho.NewFile(io.NewSectionReader(mr, 0, int64(len(huge))))
	if err != nil || f.Symtab == nil || len(f.Symtab.Syms) != 0 {
		t.Fatalf("NewFile over the bounded reader = %v, %+v", err, f)
	}
	small := symtabOnlyMachO(0, 0)
	if _, skipped := boundSymtab(bytes.NewReader(small), DefaultMaxScanBytes); skipped {
		t.Fatal("empty symtab flagged")
	}
	if mr, _ := boundSymtab(strings.NewReader("#!/bin/sh\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n"), 1); mr != nil {
		t.Fatal("non-Mach-O accepted")
	}
}

func TestMachoVerdictMarksSkippedSymbols(t *testing.T) {
	mf, err := macho.NewFile(bytes.NewReader(buildMachO(macho.CpuArm64, sect{"__TEXT", "__const", rustData()})))
	if err != nil {
		t.Fatal(err)
	}
	v := machoVerdict(mf, DefaultMaxScanBytes, true)
	if v.Runtime != RuntimeRust || !strings.Contains(v.Evidence, "symbol table too large") {
		t.Fatalf("verdict = %+v", v)
	}
}

// A skipped symbol table must still parse: debug/macho rejects a dysymtab
// that indexes past an empty symtab (seen live on iTerm2).
func TestBoundSymtabKeepsDysymtabConsistent(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	f, err := os.Open(exe)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	mr, skipped := boundSymtab(f, 0)
	if mr == nil {
		t.Skip("test binary is not a thin Mach-O")
	}
	if !skipped {
		t.Fatal("limit 0 did not skip the symbol table")
	}
	mf, err := macho.NewFile(mr)
	if err != nil {
		t.Fatalf("Mach-O with a skipped symbol table does not parse: %v", err)
	}
	if mf.Symtab != nil && len(mf.Symtab.Syms) != 0 {
		t.Fatalf("symbols were loaded: %d", len(mf.Symtab.Syms))
	}
}
