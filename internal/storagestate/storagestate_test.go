package storagestate

import (
	"os"
	"path/filepath"
	"testing"
)

const dfOutput = `Filesystem                       1024-blocks      Used Available Capacity Mounted on
/dev/disk3s1s1                   971309944 413876292 444843444    49% /
devfs                                  386       386         0   100% /dev
/dev/disk3s6                     971309944    753792 444843444     1% /System/Volumes/VM
/dev/disk3s2                     971309944  13285364 444843444     3% /System/Volumes/Preboot
/dev/disk3s4                     971309944   1107516 444843444     1% /System/Volumes/Recovery
/dev/disk3s5                     971309944   5312168 444843444     2% /data
`

const mountOutput = `/dev/disk3s1s1 on / (apfs, sealed, local, read-only, journaled)
devfs on /dev (devfs, local, nobrowse)
/dev/disk3s5 on /data (apfs, local, journaled)
`

func TestParseDF(t *testing.T) {
	vols := parseDF(dfOutput)
	// Should include /, /data but skip devfs and /System/Volumes/*
	if len(vols) != 2 {
		t.Fatalf("got %d volumes, want 2: %+v", len(vols), vols)
	}
	mounts := map[string]Volume{}
	for _, v := range vols {
		mounts[v.MountPoint] = v
	}
	if _, ok := mounts["/"]; !ok {
		t.Error("/ should be included")
	}
	if _, ok := mounts["/data"]; !ok {
		t.Error("/data should be included")
	}
	if _, ok := mounts["/dev"]; ok {
		t.Error("/dev (devfs) should be excluded")
	}
}

func TestParseDFBytes(t *testing.T) {
	vols := parseDF(dfOutput)
	root := vols[0] // "/"
	// 971309944 * 1024
	if root.TotalBytes != 971309944*1024 {
		t.Errorf("TotalBytes = %d, want %d", root.TotalBytes, 971309944*1024)
	}
}

func TestParseMountFSTypes(t *testing.T) {
	got := parseMountFSTypes(mountOutput)
	if got["/"] != "apfs" {
		t.Fatalf("root fs type = %q, want apfs", got["/"])
	}
	if got["/dev"] != "devfs" {
		t.Fatalf("/dev fs type = %q, want devfs", got["/dev"])
	}
}

func TestApplyFSTypes(t *testing.T) {
	vols := parseDF(dfOutput)
	applyFSTypes(vols, parseMountFSTypes(mountOutput))
	for _, v := range vols {
		switch v.MountPoint {
		case "/", "/data":
			if v.FSType != "apfs" {
				t.Fatalf("%s fs type = %q, want apfs", v.MountPoint, v.FSType)
			}
		}
	}
}

func TestParseDFEmpty(t *testing.T) {
	vols := parseDF("")
	if len(vols) != 0 {
		t.Errorf("expected empty for blank input")
	}
}

func TestDirBytes(t *testing.T) {
	dir := t.TempDir()
	// Write a few files.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), make([]byte, 1024), 0o644)

	bytes := dirBytes(dir)
	if bytes == 0 {
		t.Error("dirBytes should be > 0 for non-empty dir")
	}
}

func TestDirBytesMissing(t *testing.T) {
	b := dirBytes("/nonexistent/path")
	if b != 0 {
		t.Errorf("expected 0 for missing dir, got %d", b)
	}
}

func TestTopApps(t *testing.T) {
	dir := t.TempDir()
	// Create two fake "app" dirs with different sizes.
	big := filepath.Join(dir, "Big.app")
	small := filepath.Join(dir, "Small.app")
	os.MkdirAll(big, 0o755)
	os.MkdirAll(small, 0o755)
	os.WriteFile(filepath.Join(big, "exec"), make([]byte, 4096), 0o755)
	os.WriteFile(filepath.Join(small, "exec"), make([]byte, 128), 0o755)

	apps := topApps([]string{big, small}, 10)
	if len(apps) != 2 {
		t.Fatalf("got %d apps, want 2", len(apps))
	}
	// Big should be first.
	if apps[0].Path != big {
		t.Errorf("largest app = %q, want Big.app", apps[0].Path)
	}
}

func TestTopAppsLimit(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 20; i++ {
		p := filepath.Join(dir, "App.app")
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "x"), []byte("x"), 0o644)
		paths = append(paths, p)
	}
	apps := topApps(paths, 5)
	if len(apps) > 5 {
		t.Errorf("topApps(n=5) returned %d, want ≤5", len(apps))
	}
}

func TestCollect(t *testing.T) {
	home := t.TempDir()
	// Create ~/Library/Caches with one file.
	cacheDir := filepath.Join(home, "Library", "Caches")
	os.MkdirAll(cacheDir, 0o755)
	os.WriteFile(filepath.Join(cacheDir, "test.dat"), make([]byte, 2048), 0o644)

	stub := func(name string, args ...string) ([]byte, error) {
		if name == "mount" {
			return []byte(mountOutput), nil
		}
		return []byte(dfOutput), nil
	}

	s := Collect(CollectOptions{
		Home:      home,
		CmdRunner: stub,
	})
	if len(s.Volumes) == 0 {
		t.Error("expected volumes from stubbed df")
	}
	if s.Volumes[0].FSType == "" {
		t.Error("expected fs_type from stubbed mount")
	}
	if s.AppCachesBytes == 0 {
		t.Error("AppCachesBytes should be > 0")
	}
	if s.UserLibraryBytes == 0 {
		t.Error("UserLibraryBytes should be > 0")
	}
}
