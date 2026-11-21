//go:build darwin

package storagestate

import (
	"encoding/binary"
	"path/filepath"
	"sync"
	"testing"
)

// TestReadDirBulkHandlesTempDir guards against the equivalence test passing
// only because every directory silently fell back to lstat.
func TestReadDirBulkHandlesTempDir(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "a.bin"), 10_000)
	writeSized(t, filepath.Join(root, "sub", "b.bin"), 1)
	w := &treeWalker{root: root, seen: map[seenKey]struct{}{}}
	w.cond = sync.NewCond(&w.mu)
	total, ok := w.readDirBulk(walkDir{path: root}, nil)
	if !ok {
		t.Fatal("readDirBulk fell back on an ordinary directory")
	}
	if want := lstatBytes(t, filepath.Join(root, "a.bin")); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
	if len(w.queue) != 1 || filepath.Base(w.queue[0].path) != "sub" {
		t.Errorf("queue = %+v, want only sub", w.queue)
	}
}

func bulkRecord(name string, objType uint32, alloc uint64) []byte {
	ne := binary.NativeEndian
	rec := make([]byte, minFileRecLen+len(name)+1)
	ne.PutUint32(rec[0:], uint32(len(rec)))
	ne.PutUint32(rec[offReturned:], bulkCommonAttrs)
	ne.PutUint32(rec[offReturned+12:], bulkFileAttrs)
	ne.PutUint32(rec[offNameRef:], uint32(minFileRecLen-offNameRef))
	ne.PutUint32(rec[offNameRef+4:], uint32(len(name)+1))
	ne.PutUint32(rec[offObjType:], objType)
	ne.PutUint32(rec[offLinkCount:], 1)
	ne.PutUint64(rec[offAllocSize:], alloc)
	copy(rec[minFileRecLen:], name)
	return rec
}

func TestDecodeBulk(t *testing.T) {
	buf := append(bulkRecord("file", vREG, 4096), bulkRecord("dir", vDIR, 0)...)
	got, err := decodeBulk(buf, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].name != "file" || !got[0].isReg || got[0].alloc != 4096 || !got[0].hasSize ||
		got[1].name != "dir" || !got[1].isDir {
		t.Errorf("decodeBulk = %+v", got)
	}
}

func TestDecodeBulkRejectsOverruns(t *testing.T) {
	ne := binary.NativeEndian
	cases := map[string]func() ([]byte, int){
		"count beyond buffer": func() ([]byte, int) { return bulkRecord("a", vREG, 0), 2 },
		"length beyond buffer": func() ([]byte, int) {
			r := bulkRecord("a", vREG, 0)
			ne.PutUint32(r, uint32(len(r)+8))
			return r, 1
		},
		"length too short": func() ([]byte, int) {
			r := bulkRecord("a", vREG, 0)
			ne.PutUint32(r, 8)
			return r, 1
		},
		"name beyond record": func() ([]byte, int) {
			r := bulkRecord("a", vREG, 0)
			ne.PutUint32(r[offNameRef+4:], 1000)
			return r, 1
		},
		"negative name offset": func() ([]byte, int) {
			r := bulkRecord("a", vREG, 0)
			ne.PutUint32(r[offNameRef:], 0xFFFFFF00)
			return r, 1
		},
		"truncated buffer": func() ([]byte, int) { return []byte{1, 2}, 1 },
	}
	for name, mk := range cases {
		buf, n := mk()
		if _, err := decodeBulk(buf, n, nil); err == nil {
			t.Errorf("%s: decodeBulk accepted malformed input", name)
		}
	}
}
