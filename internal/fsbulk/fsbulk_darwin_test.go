//go:build darwin

package fsbulk

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestListerReadDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	var l Lister
	defer l.Release()
	got := map[string]Entry{}
	if !l.ReadDir(dir, func(e *Entry) { c := *e; got[string(e.Name)] = c }) {
		t.Fatal("ReadDir reported the bulk reader unusable")
	}
	if len(got) != 3 {
		t.Fatalf("entries = %v", got)
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(filepath.Join(dir, "f"), &st); err != nil {
		t.Fatal(err)
	}
	if f := got["f"]; !f.Complete() || f.Type != VREG || f.Size != st.Size || AllocBlocks(f.Alloc) != st.Blocks*512 {
		t.Errorf("f = %+v, lstat size %d blocks %d", f, st.Size, st.Blocks)
	}
	if d := got["d"]; !d.IsDir() || !d.Complete() {
		t.Errorf("d = %+v", d)
	}
	if s := got["l"]; s.IsDir() || s.Type == VREG {
		t.Errorf("l = %+v, want a non-regular entry", s)
	}
	if !l.ReadDir(filepath.Join(dir, "missing"), func(*Entry) { t.Error("visited a missing directory") }) {
		t.Error("a directory that cannot be opened should report handled")
	}
}

func TestDecodeEntriesRejectsMalformed(t *testing.T) {
	buf := make([]byte, entryMinLen)
	binary.NativeEndian.PutUint32(buf, uint32(len(buf)+1)) // overruns buf
	if _, err := decodeEntries(buf, 1, nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("overlong record: err = %v", err)
	}
	binary.NativeEndian.PutUint32(buf, uint32(len(buf))) // no ATTR_CMN_NAME returned
	if _, err := decodeEntries(buf, 1, nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("nameless record: err = %v", err)
	}
}
