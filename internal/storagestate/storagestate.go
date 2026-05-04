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
	Volumes         []Volume   `json:"volumes,omitempty"`
	UserLibraryBytes int64     `json:"user_library_bytes"`
	AppCachesBytes  int64      `json:"app_caches_bytes"`
	LargestApps     []AppSize  `json:"largest_apps,omitempty"`
}

// Volume is one mounted filesystem.
type Volume struct {
	MountPoint    string `json:"mount_point"`
	FSType        string `json:"fs_type,omitempty"`
	TotalBytes    int64  `json:"total_bytes"`
	UsedBytes     int64  `json:"used_bytes"`
	AvailBytes    int64  `json:"avail_bytes"`
}

// AppSize is one app bundle's on-disk footprint.
type AppSize struct {
	Path         string `json:"path"`
	OnDiskBytes  int64  `json:"on_disk_bytes"`
}

// CmdRunner abstracts shell-out for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
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
	if out, err := run("df", "-Pk"); err == nil {
		s.Volumes = parseDF(string(out))
	}
	s.UserLibraryBytes = dirBytes(filepath.Join(opts.Home, "Library"))
	s.AppCachesBytes = dirBytes(filepath.Join(opts.Home, "Library", "Caches"))
	if len(opts.AppPaths) > 0 {
		s.LargestApps = topApps(opts.AppPaths, opts.LargestAppsN)
	}
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
			strings.HasPrefix(fields[0], "map ") ||
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
			MountPoint: mount,
			TotalBytes: total,
			UsedBytes:  used,
			AvailBytes: avail,
		})
	}
	return volumes
}

// dirBytes returns the total on-disk size of all files under dir using
// sparse-file-aware block counting. Returns 0 if dir doesn't exist.
func dirBytes(dir string) int64 {
	var total int64
	filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error { //nolint:errcheck
		if err != nil || fi.IsDir() {
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
