package storagestate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

// TestMain pins the host OS to Darwin so the macOS df/mount/~Library tests
// are host-independent. Linux behavior is exercised with explicit OS
// arguments in the *Linux tests below.
func TestMain(m *testing.M) {
	restore := hostos.SetForTest(hostos.Darwin)
	code := m.Run()
	restore()
	os.Exit(code)
}

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
	vols := parseDF(dfOutput, hostos.Darwin)
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
	vols := parseDF(dfOutput, hostos.Darwin)
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
	vols := parseDF(dfOutput, hostos.Darwin)
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
	vols := parseDF("", hostos.Darwin)
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

const dfOutputLinux = `Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/nvme0n1p2   488384032 200000000 263000000      44% /
tmpfs             16384000         0  16384000       0% /dev/shm
devtmpfs           8192000         0   8192000       0% /dev
/dev/loop3           56320     56320         0     100% /snap/core
efivarfs               128        50        78      39% /sys/firmware/efi/efivars
/dev/nvme0n1p1      523248     12000    511248       3% /boot/efi
`

func TestParseDFLinux(t *testing.T) {
	vols := parseDF(dfOutputLinux, hostos.Linux)
	mounts := map[string]bool{}
	for _, v := range vols {
		mounts[v.MountPoint] = true
	}
	if !mounts["/"] || !mounts["/boot/efi"] {
		t.Errorf("expected / and /boot/efi, got %+v", mounts)
	}
	for _, skipped := range []string{"/dev/shm", "/dev", "/snap/core", "/sys/firmware/efi/efivars"} {
		if mounts[skipped] {
			t.Errorf("%s should be excluded on Linux", skipped)
		}
	}
}

func TestParseProcMounts(t *testing.T) {
	const procMounts = `/dev/nvme0n1p2 / ext4 rw,relatime 0 0
tmpfs /dev/shm tmpfs rw,nosuid 0 0
/dev/nvme0n1p1 /boot/efi vfat rw,relatime 0 0
/dev/sdb1 /mnt/my\040disk xfs rw 0 0
`
	got := parseProcMounts(procMounts)
	if got["/"] != "ext4" {
		t.Errorf("/ fstype = %q, want ext4", got["/"])
	}
	if got["/boot/efi"] != "vfat" {
		t.Errorf("/boot/efi fstype = %q, want vfat", got["/boot/efi"])
	}
	if got["/mnt/my disk"] != "xfs" {
		t.Errorf("octal-escaped mount point not decoded: %+v", got)
	}
}

func TestCollectUserFootprintLinux(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, ".cache")
	os.MkdirAll(cache, 0o755)
	os.WriteFile(filepath.Join(cache, "blob"), make([]byte, 4096), 0o644)

	lib, caches := collectUserFootprint(hostos.Linux, home)
	if lib != 0 {
		t.Errorf("UserLibraryBytes = %d, want 0 on Linux (no ~/Library)", lib)
	}
	if caches == 0 {
		t.Error("AppCachesBytes should reflect ~/.cache on Linux")
	}
}

func TestLogRootsLinux(t *testing.T) {
	roots := logRoots(hostos.Linux, "/home/u")
	want := map[string]bool{"/var/log": false, "/home/u/.local/state": false}
	for _, r := range roots {
		if _, ok := want[r]; ok {
			want[r] = true
		}
		if r == "/Library/Logs" {
			t.Error("macOS /Library/Logs should not be a Linux log root")
		}
	}
	for r, seen := range want {
		if !seen {
			t.Errorf("expected Linux log root %q", r)
		}
	}
}
