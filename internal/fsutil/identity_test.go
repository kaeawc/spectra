package fsutil

import (
	"os"
	"testing"
)

// linuxStat mimics syscall.Stat_t's Linux field names on any host.
type linuxStat struct {
	Dev, Ino uint64
	Ctim     struct{ Sec, Nsec int64 }
}

type fakeInfo struct {
	os.FileInfo
	sys any
}

func (f fakeInfo) Sys() any { return f.sys }

func TestIdentityReadsInodeAndCtime(t *testing.T) {
	st := &linuxStat{Dev: 7, Ino: 42}
	st.Ctim.Sec, st.Ctim.Nsec = 3, 4
	if got := Identity(fakeInfo{sys: st}); got != (FileID{Dev: 7, Ino: 42, Ctime: 3e9 + 4}) {
		t.Fatalf("linux-shaped identity = %+v", got)
	}
	fi, err := os.Stat(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := Identity(fi); got.Ino == 0 || got.Ctime == 0 {
		t.Fatalf("host identity = %+v, want inode and ctime", got)
	}
	if got := Identity(fakeInfo{}); got != (FileID{}) {
		t.Fatalf("no Sys identity = %+v", got)
	}
}
