package storagestate

import (
	"errors"
	"testing"
)

func TestSpotlightForPath(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "df" && len(args) == 2 && args[1] == "/Users/tester/wt":
			return []byte("Filesystem   1024-blocks Used Available Capacity  Mounted on\n/dev/disk3s5 100 50 50 50%    /System/Volumes/Data\n"), nil
		case name == "/usr/bin/mdutil" && len(args) == 2 && args[0] == "-s" && args[1] == "/System/Volumes/Data":
			return []byte("/System/Volumes/Data:\n\tIndexing enabled. \n"), nil
		case name == "/usr/bin/mdutil" && len(args) == 1 && args[0] == "-p":
			t.Error("mdutil -p flushes caches and must not run")
		}
		return nil, errors.New("unexpected " + name)
	}
	v, err := SpotlightForPath(run, "/Users/tester/wt")
	if err != nil {
		t.Fatal(err)
	}
	if v.MountPoint != "/System/Volumes/Data" || v.Status != SpotlightEnabled || v.Progress != nil {
		t.Fatalf("volume = %+v", v)
	}
	if _, err := SpotlightForPath(run, "/elsewhere"); err == nil {
		t.Fatal("want df error")
	}
	if got := dfMountPoint("Filesystem x\n/dev/disk4s1 1 1 1 1% /Volumes/My Disk\n"); got != "/Volumes/My Disk" {
		t.Fatalf("mount = %q", got)
	}
}
