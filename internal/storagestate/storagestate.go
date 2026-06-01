// Package storagestate captures a point-in-time snapshot of disk volumes
// and the user's ~/Library directory. All sizing is sparse-file-aware
// (Stat_t.Blocks * 512) so Docker-style thin containers don't inflate
// the numbers. See docs/design/system-inventory.md#storagestate.
package storagestate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// State is the StorageState slice of a Spectra snapshot.
type State struct {
	Volumes          []Volume          `json:"volumes,omitempty"`
	Mounts           []Mount           `json:"mounts,omitempty"`
	Spotlight        []SpotlightVolume `json:"spotlight,omitempty"`
	UserLibraryBytes int64             `json:"user_library_bytes"`
	AppCachesBytes   int64             `json:"app_caches_bytes"`
	LargestApps      []AppSize         `json:"largest_apps,omitempty"`
	LogFiles         []LogFile         `json:"log_files,omitempty"`
}

// Volume is one mounted filesystem.
type Volume struct {
	Device     string         `json:"device,omitempty"`
	MountPoint string         `json:"mount_point"`
	FSType     string         `json:"fs_type,omitempty"`
	ReadOnly   bool           `json:"read_only,omitempty"`
	TotalBytes int64          `json:"total_bytes"`
	UsedBytes  int64          `json:"used_bytes"`
	AvailBytes int64          `json:"avail_bytes"`
	Snapshots  []APFSSnapshot `json:"snapshots,omitempty"`
}

// Mount is one currently mounted filesystem from getfsstat.
type Mount struct {
	Device     string   `json:"device,omitempty"`
	MountPoint string   `json:"mount_point"`
	FSType     string   `json:"fs_type,omitempty"`
	Flags      []string `json:"flags,omitempty"`
	Sealed     bool     `json:"sealed,omitempty"`
	ReadOnly   bool     `json:"read_only,omitempty"`
	APFSRole   string   `json:"apfs_role,omitempty"`
	BlockSize  uint32   `json:"block_size,omitempty"`
	Capacity   Capacity `json:"capacity"`
}

// Capacity is per-mount capacity from statfs.
type Capacity struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Available   uint64  `json:"available"`
	UsedPercent float64 `json:"used_percent"`
	Inodes      uint64  `json:"inodes,omitempty"`
	InodesFree  uint64  `json:"inodes_free,omitempty"`
}

// AppSize is one app bundle's on-disk footprint.
type AppSize struct {
	Path        string `json:"path"`
	OnDiskBytes int64  `json:"on_disk_bytes"`
}

// CmdRunner abstracts shell-out for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(_ string, _ ...string) ([]byte, error) {
	return os.ReadFile("/dev/null") // never called directly; overridden per-use
}

// CollectOptions parameterises the collector.
type CollectOptions struct {
	// Home is the user's home directory. Defaults to os.UserHomeDir().
	Home string
	// AppPaths is the list of .app bundles to include in LargestApps.
	// Typically populated from Snapshot.Apps[i].Path.
	AppPaths []string
	// LargestAppsN is how many top apps to report (default 10).
	LargestAppsN int
	// CmdRunner overrides exec.Command for testing.
	CmdRunner CmdRunner
	// IncludeSnapshots runs diskutil APFS snapshot collection for APFS volumes.
	IncludeSnapshots bool
	// IncludeSpotlight runs read-only mdutil Spotlight status collection.
	IncludeSpotlight bool
}

// Collect gathers StorageState.
func Collect(opts CollectOptions) State {
	if opts.Home == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	if opts.LargestAppsN == 0 {
		opts.LargestAppsN = 10
	}
	run := opts.CmdRunner
	if run == nil {
		run = execRunner
	}

	var s State
	if mounts, err := collectMounts(run); err == nil {
		s.Mounts = mounts
	}
	if out, err := run("df", "-Pk"); err == nil {
		s.Volumes = parseDF(string(out))
	}
	if len(s.Volumes) > 0 {
		if out, err := run("mount"); err == nil {
			applyMountInfo(s.Volumes, parseMountInfos(string(out)))
		}
	}
	if opts.IncludeSnapshots {
		applyAPFSSnapshots(s.Volumes, run)
	}
	if opts.IncludeSpotlight {
		s.Spotlight = collectSpotlight(run, s.Mounts)
	}
	s.UserLibraryBytes = dirBytes(filepath.Join(opts.Home, "Library"))
	s.AppCachesBytes = dirBytes(filepath.Join(opts.Home, "Library", "Caches"))
	if len(opts.AppPaths) > 0 {
		s.LargestApps = topApps(opts.AppPaths, opts.LargestAppsN)
	}
	s.LogFiles = CollectLogFiles(opts.Home)
	return s
}

// parseDF converts `df -Pk` output to Volume slices.
// POSIX df output: Filesystem 1024-blocks Used Available Capacity% Mounted-on
func parseDF(out string) []Volume {
	var volumes []Volume
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		mount := fields[5]
		// Skip pseudo filesystems.
		if strings.HasPrefix(fields[0], "devfs") ||
			fields[0] == "map" ||
			strings.HasPrefix(mount, "/dev") ||
			strings.HasPrefix(mount, "/System/Volumes/Preboot") ||
			strings.HasPrefix(mount, "/System/Volumes/Recovery") ||
			strings.HasPrefix(mount, "/System/Volumes/VM") ||
			strings.HasPrefix(mount, "/System/Volumes/xarts") {
			continue
		}
		total := parseInt64(fields[1]) * 1024
		used := parseInt64(fields[2]) * 1024
		avail := parseInt64(fields[3]) * 1024
		volumes = append(volumes, Volume{
			Device:     fields[0],
			MountPoint: mount,
			TotalBytes: total,
			UsedBytes:  used,
			AvailBytes: avail,
		})
	}
	return volumes
}

func applyFSTypes(volumes []Volume, fsTypes map[string]string) {
	for i := range volumes {
		if fsType := fsTypes[volumes[i].MountPoint]; fsType != "" {
			volumes[i].FSType = fsType
		}
	}
}

func applyMountInfo(volumes []Volume, infos map[string]mountInfo) {
	for i := range volumes {
		if info, ok := infos[volumes[i].MountPoint]; ok {
			volumes[i].FSType = info.fsType
			volumes[i].ReadOnly = info.readOnly
		}
	}
}

func parseMountFSTypes(out string) map[string]string {
	fsTypes := map[string]string{}
	for mountPoint, info := range parseMountInfos(out) {
		fsTypes[mountPoint] = info.fsType
	}
	return fsTypes
}

type mountInfo struct {
	fsType   string
	readOnly bool
}

func parseMountInfos(out string) map[string]mountInfo {
	infos := map[string]mountInfo{}
	for _, line := range strings.Split(out, "\n") {
		mountPoint, info, ok := parseMountLine(line)
		if ok {
			infos[mountPoint] = info
		}
	}
	return infos
}

func parseMountLine(line string) (mountPoint string, info mountInfo, ok bool) {
	_, rest, ok := strings.Cut(line, " on ")
	if !ok {
		return "", mountInfo{}, false
	}
	mountPoint, options, ok := strings.Cut(rest, " (")
	if !ok {
		return "", mountInfo{}, false
	}
	fields := strings.Split(options, ",")
	if len(fields) == 0 {
		return "", mountInfo{}, false
	}
	info.fsType = strings.TrimSpace(strings.TrimSuffix(fields[0], ")"))
	for _, field := range fields[1:] {
		if strings.TrimSpace(strings.TrimSuffix(field, ")")) == "read-only" {
			info.readOnly = true
		}
	}
	mountPoint = strings.TrimSpace(mountPoint)
	if mountPoint == "" || info.fsType == "" {
		return "", mountInfo{}, false
	}
	return mountPoint, info, true
}

// dirBytes returns the total on-disk size of all files under dir using
// sparse-file-aware block counting. Returns 0 if dir doesn't exist.
func dirBytes(dir string) int64 {
	var total int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, _ error) error { //nolint:errcheck
		if fi == nil || fi.IsDir() {
			return nil
		}
		total += diskBytes(fi)
		return nil
	})
	return total
}

// topApps returns the top-N app paths by on-disk size, sorted descending.
func topApps(paths []string, n int) []AppSize {
	sizes := make([]AppSize, 0, len(paths))
	for _, p := range paths {
		b := dirBytes(p)
		if b > 0 {
			sizes = append(sizes, AppSize{Path: p, OnDiskBytes: b})
		}
	}
	sort.Slice(sizes, func(i, j int) bool {
		return sizes[i].OnDiskBytes > sizes[j].OnDiskBytes
	})
	if len(sizes) > n {
		sizes = sizes[:n]
	}
	return sizes
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
