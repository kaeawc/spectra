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

	"github.com/kaeawc/spectra/internal/hostos"
)

// State is the StorageState slice of a Spectra snapshot.
type State struct {
	Volumes          []Volume  `json:"volumes,omitempty"`
	UserLibraryBytes int64     `json:"user_library_bytes"`
	AppCachesBytes   int64     `json:"app_caches_bytes"`
	LargestApps      []AppSize `json:"largest_apps,omitempty"`
	LogFiles         []LogFile `json:"log_files,omitempty"`
}

// Volume is one mounted filesystem.
type Volume struct {
	MountPoint string `json:"mount_point"`
	FSType     string `json:"fs_type,omitempty"`
	TotalBytes int64  `json:"total_bytes"`
	UsedBytes  int64  `json:"used_bytes"`
	AvailBytes int64  `json:"avail_bytes"`
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
	// OS selects OS-specific behavior (df pseudo-volume filtering, the
	// fstype source, and log/cache roots). Zero value resolves to host.
	OS hostos.Kind
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
	osKind := hostos.Resolve(opts.OS)

	var s State
	if out, err := run("df", "-Pk"); err == nil {
		s.Volumes = parseDF(string(out), osKind)
	}
	if len(s.Volumes) > 0 {
		applyFSTypes(s.Volumes, collectFSTypes(osKind, run))
	}
	s.UserLibraryBytes, s.AppCachesBytes = collectUserFootprint(osKind, opts.Home)
	if len(opts.AppPaths) > 0 {
		s.LargestApps = topApps(opts.AppPaths, opts.LargestAppsN)
	}
	s.LogFiles = CollectLogFiles(opts.Home, osKind)
	return s
}

// collectFSTypes returns mount-point → filesystem-type. macOS parses
// mount(8) output; Linux reads /proc/mounts (mount(8)'s output format
// differs between the two, and /proc/mounts needs no subprocess).
func collectFSTypes(osKind hostos.Kind, run CmdRunner) map[string]string {
	if osKind == hostos.Linux {
		if data, err := os.ReadFile("/proc/mounts"); err == nil {
			return parseProcMounts(string(data))
		}
		return nil
	}
	if out, err := run("mount"); err == nil {
		return parseMountFSTypes(string(out))
	}
	return nil
}

// collectUserFootprint returns the user's library and cache on-disk sizes.
// macOS uses ~/Library and ~/Library/Caches; Linux has no ~/Library, so
// library size is left zero and cache maps to the XDG cache dir.
func collectUserFootprint(osKind hostos.Kind, home string) (library, caches int64) {
	if osKind == hostos.Linux {
		cacheDir := filepath.Join(home, ".cache")
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			cacheDir = xdg
		}
		return 0, dirBytes(cacheDir)
	}
	return dirBytes(filepath.Join(home, "Library")),
		dirBytes(filepath.Join(home, "Library", "Caches"))
}

// parseProcMounts converts /proc/mounts lines (spec mountpoint fstype
// options dump pass) to a mount-point → fstype map.
func parseProcMounts(out string) map[string]string {
	fsTypes := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// /proc/mounts octal-escapes spaces in the mount point as \040.
		mount := strings.ReplaceAll(fields[1], `\040`, " ")
		fsTypes[mount] = fields[2]
	}
	return fsTypes
}

// parseDF converts `df -Pk` output to Volume slices.
// POSIX df output: Filesystem 1024-blocks Used Available Capacity% Mounted-on
func parseDF(out string, osKind hostos.Kind) []Volume {
	var volumes []Volume
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[0] == "Filesystem" {
			continue
		}
		mount := fields[5]
		if skipDFVolume(osKind, fields[0], mount) {
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

// skipDFVolume reports whether a df row is a pseudo/irrelevant filesystem
// that should be excluded from the volume list, per host OS.
func skipDFVolume(osKind hostos.Kind, source, mount string) bool {
	if osKind == hostos.Linux {
		switch source {
		case "tmpfs", "devtmpfs", "efivarfs", "devfs":
			return true
		}
		if strings.HasPrefix(source, "/dev/loop") { // snap squashfs images
			return true
		}
		return strings.HasPrefix(mount, "/proc") ||
			strings.HasPrefix(mount, "/sys") ||
			strings.HasPrefix(mount, "/dev") ||
			strings.HasPrefix(mount, "/run")
	}
	// macOS (and other): APFS system volumes and devfs/autofs maps.
	return strings.HasPrefix(source, "devfs") ||
		strings.HasPrefix(source, "map ") ||
		strings.HasPrefix(mount, "/dev") ||
		strings.HasPrefix(mount, "/System/Volumes/Preboot") ||
		strings.HasPrefix(mount, "/System/Volumes/Recovery") ||
		strings.HasPrefix(mount, "/System/Volumes/VM") ||
		strings.HasPrefix(mount, "/System/Volumes/xarts")
}

func applyFSTypes(volumes []Volume, fsTypes map[string]string) {
	for i := range volumes {
		if fsType := fsTypes[volumes[i].MountPoint]; fsType != "" {
			volumes[i].FSType = fsType
		}
	}
}

func parseMountFSTypes(out string) map[string]string {
	fsTypes := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		mountPoint, fsType, ok := parseMountLine(line)
		if ok {
			fsTypes[mountPoint] = fsType
		}
	}
	return fsTypes
}

func parseMountLine(line string) (mountPoint string, fsType string, ok bool) {
	_, rest, ok := strings.Cut(line, " on ")
	if !ok {
		return "", "", false
	}
	mountPoint, options, ok := strings.Cut(rest, " (")
	if !ok {
		return "", "", false
	}
	fields := strings.Split(options, ",")
	if len(fields) == 0 {
		return "", "", false
	}
	fsType = strings.TrimSpace(strings.TrimSuffix(fields[0], ")"))
	mountPoint = strings.TrimSpace(mountPoint)
	if mountPoint == "" || fsType == "" {
		return "", "", false
	}
	return mountPoint, fsType, true
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
