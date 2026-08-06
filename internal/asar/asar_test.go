package asar

import (
	"encoding/binary"
	"testing"
)

// buildASAR wraps a header-directory JSON string in the 16-byte Chromium
// pickle prefix an asar file begins with (only the length at offset 12 is read
// by the parser, but we fill the other fields to mirror the real layout).
func buildASAR(headerJSON string) []byte {
	jb := []byte(headerJSON)
	buf := make([]byte, 16+len(jb))
	binary.LittleEndian.PutUint32(buf[0:], 4)
	binary.LittleEndian.PutUint32(buf[4:], uint32(len(jb)+8))
	binary.LittleEndian.PutUint32(buf[8:], uint32(len(jb)+4))
	binary.LittleEndian.PutUint32(buf[12:], uint32(len(jb)))
	copy(buf[16:], jb)
	return buf
}

const sampleHeader = `{"files":{
  "main.js":{"size":100,"offset":"0","integrity":{"algorithm":"SHA256","hash":"aaa"}},
  "node_modules":{"files":{"keytar":{"files":{"build":{"files":{
    "keytar.node":{"size":2000,"offset":"100","unpacked":true,"integrity":{"algorithm":"SHA256","hash":"bbb"}}
  }}}}}}
}}`

func TestParseInventory(t *testing.T) {
	a, err := Parse(buildASAR(sampleHeader))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Files) != 2 {
		t.Fatalf("files = %d, want 2: %+v", len(a.Files), a.Files)
	}
	// sorted by path: main.js first
	if a.Files[0].Path != "main.js" || a.Files[0].SHA256 != "aaa" || a.Files[0].Size != 100 {
		t.Errorf("entry0 = %+v", a.Files[0])
	}
	kt := a.Files[1]
	if kt.Path != "node_modules/keytar/build/keytar.node" {
		t.Errorf("entry1 path = %q", kt.Path)
	}
	if !kt.Unpacked || kt.SHA256 != "bbb" {
		t.Errorf("keytar.node entry = %+v, want unpacked + sha bbb", kt)
	}
}

func TestNativeModulePaths(t *testing.T) {
	a, _ := Parse(buildASAR(sampleHeader))
	nm := a.NativeModulePaths()
	if len(nm) != 1 || nm[0] != "node_modules/keytar/build/keytar.node" {
		t.Errorf("native modules = %v", nm)
	}
}

func TestParseTooShort(t *testing.T) {
	if _, err := Parse([]byte("tiny")); err == nil {
		t.Fatal("expected error for short input")
	}
}

func TestParseImplausibleLength(t *testing.T) {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint32(buf[12:], 0xFFFFFFFF)
	if _, err := Parse(buf); err == nil {
		t.Fatal("expected error for implausible header size")
	}
}

func TestParseLengthExceedsData(t *testing.T) {
	buf := make([]byte, 20)
	binary.LittleEndian.PutUint32(buf[12:], 1000) // claims 1000 bytes of json but only 4 present
	if _, err := Parse(buf); err == nil {
		t.Fatal("expected error when header length exceeds data")
	}
}

func TestDiffArchives(t *testing.T) {
	oldA, _ := Parse(buildASAR(`{"files":{
      "main.js":{"size":100,"integrity":{"hash":"aaa"}},
      "gone.js":{"size":10,"integrity":{"hash":"xxx"}}
    }}`))
	newA, _ := Parse(buildASAR(`{"files":{
      "main.js":{"size":120,"integrity":{"hash":"ccc"}},
      "new.js":{"size":50,"integrity":{"hash":"ddd"}}
    }}`))
	d := DiffArchives(oldA, newA)
	if d.Empty() {
		t.Fatal("expected a non-empty diff")
	}
	if len(d.Added) != 1 || d.Added[0].Path != "new.js" {
		t.Errorf("added = %+v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Path != "gone.js" {
		t.Errorf("removed = %+v", d.Removed)
	}
	if len(d.Changed) != 1 || d.Changed[0].Path != "main.js" {
		t.Errorf("changed = %+v", d.Changed)
	}
}

func TestDiffIdentical(t *testing.T) {
	a, _ := Parse(buildASAR(sampleHeader))
	b, _ := Parse(buildASAR(sampleHeader))
	if !DiffArchives(a, b).Empty() {
		t.Error("identical archives should diff empty")
	}
}
