//go:build darwin

package storagestate

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestConvertStatfsRootFlagsAndRole(t *testing.T) {
	raw := statfsFixture("/", "/dev/disk1s1", "apfs", unix.MNT_ROOTFS|unix.MNT_RDONLY|unix.MNT_LOCAL|unix.MNT_JOURNALED|unix.MNT_SNAPSHOT)
	raw.Blocks = 100
	raw.Bfree = 10
	raw.Bavail = 8
	raw.Files = 1000
	raw.Ffree = 200

	m := convertStatfs(&raw, nil)
	if m.APFSRole != "system" {
		t.Fatalf("APFSRole = %q, want system", m.APFSRole)
	}
	if !m.ReadOnly || !m.Sealed {
		t.Fatalf("ReadOnly=%v Sealed=%v, want both true", m.ReadOnly, m.Sealed)
	}
	for _, flag := range []string{"sealed", "ro", "journaled", "local"} {
		if !slices.Contains(m.Flags, flag) {
			t.Fatalf("flags = %v, missing %q", m.Flags, flag)
		}
	}
	if m.Capacity.Total != 409600 || m.Capacity.Used != 368640 || m.Capacity.Available != 32768 {
		t.Fatalf("capacity = %+v", m.Capacity)
	}
}

func TestConvertStatfsDataAndSimulatorRoles(t *testing.T) {
	data := statfsFixture("/System/Volumes/Data", "/dev/disk1s5", "apfs", unix.MNT_LOCAL|unix.MNT_JOURNALED)
	data.Flags_ext = unix.MNT_EXT_ROOT_DATA_VOL
	if got := convertStatfs(&data, nil).APFSRole; got != "data" {
		t.Fatalf("data role = %q, want data", got)
	}

	sim := statfsFixture("/Library/Developer/CoreSimulator/Volumes/iOS", "/dev/disk5s1", "apfs", unix.MNT_RDONLY|unix.MNT_LOCAL)
	if got := convertStatfs(&sim, nil).APFSRole; got != "simulator" {
		t.Fatalf("simulator role = %q, want simulator", got)
	}
}

func TestParseDiskutilInfo(t *testing.T) {
	data := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>VolumeRole</key><string>Data</string><key>Sealed</key><string>Yes</string><key>TotalSize</key><integer>1000</integer><key>CapacityInUse</key><integer>900</integer><key>APFSContainerFree</key><integer>100</integer></dict></plist>`)
	info := parseDiskutilInfo(data)
	if info.role != "data" || !info.sealed {
		t.Fatalf("info = %+v, want data sealed", info)
	}
	if !info.hasCapacity || info.capacity.UsedPercent != 90 {
		t.Fatalf("capacity = %+v, has=%v, want 90%%", info.capacity, info.hasCapacity)
	}
}

func statfsFixture(mountPoint, device, fsType string, flags uint32) unix.Statfs_t {
	var raw unix.Statfs_t
	raw.Bsize = 4096
	raw.Blocks = 100
	raw.Bfree = 50
	raw.Bavail = 50
	raw.Flags = flags
	copy(raw.Mntonname[:], mountPoint)
	copy(raw.Mntfromname[:], device)
	copy(raw.Fstypename[:], fsType)
	return raw
}

func TestConvertStatfsAllRunsDiskutilConcurrentlyInOrder(t *testing.T) {
	const n = 12
	raw := make([]unix.Statfs_t, n)
	for i := range raw {
		raw[i] = statfsFixture(fmt.Sprintf("/Volumes/conc-%d", i), fmt.Sprintf("/dev/disk-conc-test-%d", i), "apfs", unix.MNT_LOCAL)
	}
	var (
		mu       sync.Mutex
		inFlight int
		peak     int
		overlap  = make(chan struct{})
		once     sync.Once
	)
	run := func(string, ...string) ([]byte, error) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		if inFlight >= 2 {
			once.Do(func() { close(overlap) })
		}
		mu.Unlock()
		// A serial loop never overlaps, so it falls through after the timeout
		// and the peak assertion below fails instead of hanging.
		select {
		case <-overlap:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>VolumeRole</key><string>Data</string></dict></plist>`), nil
	}

	mounts := convertStatfsAll(raw, run)
	if peak < 2 || peak > maxDiskutilWorkers {
		t.Fatalf("peak concurrent diskutil calls = %d, want 2..%d", peak, maxDiskutilWorkers)
	}
	if len(mounts) != n {
		t.Fatalf("len = %d, want %d", len(mounts), n)
	}
	for i, m := range mounts {
		if want := fmt.Sprintf("/Volumes/conc-%d", i); m.MountPoint != want || m.APFSRole != "data" {
			t.Fatalf("mounts[%d] = %q role %q, want %q role data", i, m.MountPoint, m.APFSRole, want)
		}
	}
}
