//go:build darwin

package storagestate

import (
	"bytes"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	"howett.net/plist"
)

type apfsRoleInfo struct {
	role        string
	sealed      bool
	capacity    Capacity
	hasCapacity bool
}

var mountRoleCache sync.Map

func collectMounts(run CmdRunner) ([]Mount, error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	raw := make([]unix.Statfs_t, n)
	n, err = unix.Getfsstat(raw, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	out := make([]Mount, 0, n)
	for i := range raw[:n] {
		out = append(out, convertStatfs(&raw[i], run))
	}
	return out, nil
}

func convertStatfs(raw *unix.Statfs_t, run CmdRunner) Mount {
	m := Mount{
		Device:     cString(raw.Mntfromname[:]),
		MountPoint: cString(raw.Mntonname[:]),
		FSType:     cString(raw.Fstypename[:]),
		BlockSize:  raw.Bsize,
		Capacity:   capacityFromStatfs(raw),
	}
	m.ReadOnly = raw.Flags&unix.MNT_RDONLY != 0
	info := roleInfoForMount(m, raw, run)
	m.APFSRole = info.role
	m.Sealed = info.sealed
	if info.hasCapacity {
		m.Capacity = info.capacity
	}
	m.Flags = mountFlagNames(raw.Flags, m.Sealed)
	return m
}

func capacityFromStatfs(raw *unix.Statfs_t) Capacity {
	blockSize := uint64(raw.Bsize)
	total := raw.Blocks * blockSize
	available := raw.Bavail * blockSize
	used := uint64(0)
	if raw.Blocks > raw.Bfree {
		used = (raw.Blocks - raw.Bfree) * blockSize
	}
	return Capacity{
		Total:       total,
		Used:        used,
		Available:   available,
		UsedPercent: usedPercent(used, total),
		Inodes:      raw.Files,
		InodesFree:  raw.Ffree,
	}
}

func usedPercent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func mountFlagNames(flags uint32, sealed bool) []string {
	var out []string
	if sealed {
		out = append(out, "sealed")
	}
	for _, item := range []struct {
		bit  uint32
		name string
	}{
		{unix.MNT_RDONLY, "ro"},
		{unix.MNT_NOEXEC, "noexec"},
		{unix.MNT_DONTBROWSE, "nobrowse"},
		{unix.MNT_JOURNALED, "journaled"},
		{unix.MNT_LOCAL, "local"},
		{unix.MNT_NOSUID, "nosuid"},
		{unix.MNT_NODEV, "nodev"},
	} {
		if flags&item.bit != 0 {
			out = append(out, item.name)
		}
	}
	return out
}

func roleInfoForMount(m Mount, raw *unix.Statfs_t, run CmdRunner) apfsRoleInfo {
	if m.FSType != "apfs" {
		return apfsRoleInfo{}
	}
	if strings.HasPrefix(m.MountPoint, "/Library/Developer/CoreSimulator/") {
		info := apfsInfoFromDiskutil(m.Device, m.MountPoint, run)
		info.role = "simulator"
		return info
	}
	info := apfsInfoFromDiskutil(m.Device, m.MountPoint, run)
	if info.role == "" {
		info.role = inferAPFSRole(m.MountPoint, raw.Flags, raw.Flags_ext)
	}
	if raw.Flags&unix.MNT_SNAPSHOT != 0 {
		info.sealed = true
	}
	return info
}

func inferAPFSRole(mountPoint string, flags, flagsExt uint32) string {
	switch {
	case flags&unix.MNT_ROOTFS != 0 || mountPoint == "/":
		return "system"
	case flagsExt&unix.MNT_EXT_ROOT_DATA_VOL != 0 || mountPoint == "/System/Volumes/Data":
		return "data"
	case mountPoint == "/System/Volumes/Preboot":
		return "preboot"
	case mountPoint == "/System/Volumes/VM":
		return "vm"
	case strings.HasPrefix(mountPoint, "/System/Volumes/Update"):
		return "update"
	default:
		return ""
	}
}

func apfsInfoFromDiskutil(device, mountPoint string, run CmdRunner) apfsRoleInfo {
	key := device
	if key == "" {
		key = mountPoint
	}
	if key == "" {
		return apfsRoleInfo{}
	}
	if v, ok := mountRoleCache.Load(key); ok {
		return v.(apfsRoleInfo)
	}
	info := readAPFSInfo(key, run)
	mountRoleCache.Store(key, info)
	return info
}

func readAPFSInfo(target string, run CmdRunner) apfsRoleInfo {
	if run == nil {
		return apfsRoleInfo{}
	}
	out, err := run("diskutil", "info", "-plist", target)
	if err != nil {
		return apfsRoleInfo{}
	}
	info := parseDiskutilInfo(out)
	return info
}

func parseDiskutilInfo(data []byte) apfsRoleInfo {
	var root any
	if _, err := plist.Unmarshal(data, &root); err != nil {
		return apfsRoleInfo{}
	}
	m, ok := root.(map[string]any)
	if !ok {
		return apfsRoleInfo{}
	}
	info := apfsRoleInfo{
		role:   normalizeAPFSRole(stringValue(m, "VolumeRole", "APFSVolumeRole", "Role")),
		sealed: sealedValue(stringValue(m, "Sealed", "APFSSealed")),
	}
	if capacity, ok := capacityFromDiskutilInfo(m); ok {
		info.capacity = capacity
		info.hasCapacity = true
	}
	return info
}

func normalizeAPFSRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	role = strings.TrimPrefix(role, "apfs ")
	role = strings.TrimPrefix(role, "role ")
	switch role {
	case "system", "data", "preboot", "vm", "update":
		return role
	default:
		return ""
	}
}

func sealedValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && value != "no" && value != "false" && value != "0"
}

func capacityFromDiskutilInfo(m map[string]any) (Capacity, bool) {
	used := uintValue(m, "CapacityInUse")
	total := uintValue(m, "TotalSize", "Size")
	available := uintValue(m, "APFSContainerFree", "FreeSpace")
	if used == 0 || total == 0 {
		return Capacity{}, false
	}
	percentBase := total
	if used+available > 0 {
		percentBase = used + available
	}
	return Capacity{
		Total:       total,
		Used:        used,
		Available:   available,
		UsedPercent: usedPercent(used, percentBase),
	}, true
}

func cString(buf []byte) string {
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		return string(buf[:i])
	}
	return string(buf)
}
