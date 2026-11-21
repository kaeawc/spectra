package storagestate

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// libraryTopN is how many top-level ~/Library folders the breakdown names
// before rolling the rest into "other".
const libraryTopN = 12

// OtherDirName labels the rolled-up tail of UserLibraryTop.
const OtherDirName = "other"

// Reclaimable ~/Library areas, relative to ~/Library. Each is regenerable
// developer data that Xcode or the simulator rebuilds or re-downloads.
const (
	AreaDerivedData      = "Developer/Xcode/DerivedData"
	AreaCoreSimulator    = "Developer/CoreSimulator"
	AreaIOSDeviceSupport = "Developer/Xcode/iOS DeviceSupport"
	AreaXcodeArchives    = "Developer/Xcode/Archives"
)

var reclaimableAreas = []string{AreaDerivedData, AreaCoreSimulator, AreaIOSDeviceSupport, AreaXcodeArchives}

// DirSize is one named directory's on-disk usage.
type DirSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Footprint is the user's library and cache on-disk usage at one moment.
type Footprint struct {
	LibraryBytes int64     `json:"library_bytes"`
	CachesBytes  int64     `json:"caches_bytes"`
	SkippedDirs  int       `json:"skipped_dirs,omitempty"`
	AsOf         time.Time `json:"as_of"`
	// Top and Reclaimable are macOS-only; see State.UserLibraryTop and
	// State.UserLibraryReclaimable.
	Top         []DirSize `json:"top,omitempty"`
	Reclaimable []DirSize `json:"reclaimable,omitempty"`
}

// FootprintRoot is the directory the footprint walk starts from: ~/Library
// on macOS, the XDG cache dir ($XDG_CACHE_HOME or ~/.cache) on Linux.
func FootprintRoot(osKind hostos.Kind, home string) string {
	if hostos.Resolve(osKind) == hostos.Linux {
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			return xdg
		}
		return filepath.Join(home, ".cache")
	}
	return filepath.Join(home, "Library")
}

// CollectFootprint walks the footprint root once. On macOS that one pass
// yields the ~/Library total, the Caches subtotal, the per-folder breakdown,
// and the reclaimable areas. Linux has no ~/Library: only the cache dir is
// walked and LibraryBytes stays zero. now stamps the result; nil uses
// time.Now.
func CollectFootprint(osKind hostos.Kind, home string, now func() time.Time) Footprint {
	if now == nil {
		now = time.Now
	}
	osKind = hostos.Resolve(osKind)
	root := FootprintRoot(osKind, home)
	if osKind == hostos.Linux {
		u := walkTree(root, root)
		return Footprint{CachesBytes: u.Tracked[0], SkippedDirs: u.SkippedDirs, AsOf: now().UTC()}
	}

	tracked := []string{filepath.Join(root, "Caches")}
	for _, area := range reclaimableAreas {
		tracked = append(tracked, filepath.Join(root, filepath.FromSlash(area)))
	}
	u := walkTree(root, tracked...)
	fp := Footprint{
		LibraryBytes: u.Total,
		CachesBytes:  u.Tracked[0],
		SkippedDirs:  u.SkippedDirs,
		AsOf:         now().UTC(),
		Top:          topDirs(u.Top, libraryTopN),
	}
	for i, area := range reclaimableAreas {
		if b := u.Tracked[i+1]; b > 0 {
			fp.Reclaimable = append(fp.Reclaimable, DirSize{Name: area, Bytes: b})
		}
	}
	sortDirSizes(fp.Reclaimable)
	return fp
}

// topDirs ranks per-folder usage and rolls everything past n (plus files
// directly in the root, keyed "") into OtherDirName.
func topDirs(byName map[string]int64, n int) []DirSize {
	var dirs []DirSize
	var other int64
	for name, b := range byName {
		if name == "" {
			other += b
			continue
		}
		if b > 0 {
			dirs = append(dirs, DirSize{Name: name, Bytes: b})
		}
	}
	sortDirSizes(dirs)
	if len(dirs) > n {
		for _, d := range dirs[n:] {
			other += d.Bytes
		}
		dirs = dirs[:n]
	}
	if other > 0 {
		dirs = append(dirs, DirSize{Name: OtherDirName, Bytes: other})
	}
	return dirs
}

func sortDirSizes(dirs []DirSize) {
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].Bytes != dirs[j].Bytes {
			return dirs[i].Bytes > dirs[j].Bytes
		}
		return dirs[i].Name < dirs[j].Name
	})
}

// ApplyFootprint copies fp into s, marking it stale when requested.
func (s *State) ApplyFootprint(fp Footprint, stale bool) {
	s.UserLibraryBytes = fp.LibraryBytes
	s.AppCachesBytes = fp.CachesBytes
	s.UserFootprintSkippedDirs = fp.SkippedDirs
	s.UserFootprintAsOf = fp.AsOf
	s.UserFootprintStale = stale
	s.UserLibraryTop = fp.Top
	s.UserLibraryReclaimable = fp.Reclaimable
}

// collectUserFootprint returns the user's library and cache on-disk sizes.
func collectUserFootprint(osKind hostos.Kind, home string) (library, caches int64) {
	fp := CollectFootprint(osKind, home, nil)
	return fp.LibraryBytes, fp.CachesBytes
}
