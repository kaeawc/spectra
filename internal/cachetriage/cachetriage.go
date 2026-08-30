// Package cachetriage classifies the subdirectories of a cache root (typically
// ~/Library/Caches) as safe, regenerable, or risky to delete, and reports how
// much space could be reclaimed. It only reads the filesystem — it never
// deletes anything.
package cachetriage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Class is the reclamation classification of a cache subdirectory.
type Class string

const (
	// ClassSafe is a pure build/tooling cache that fully reconstructs from
	// source or network with no user-visible loss.
	ClassSafe Class = "safe"
	// ClassRegenerable is an ordinary app runtime cache: safe to delete, the
	// app rebuilds it. This is the default when nothing else matches.
	ClassRegenerable Class = "regenerable"
	// ClassRisky holds markers of real, non-regenerable data mislabeled as
	// cache; the user should review it before deleting.
	ClassRisky Class = "risky"
)

// scanFileLimit bounds the per-subdirectory walk so a pathological tree cannot
// stall triage; sizing stops contributing markers past it but keeps summing.
const scanFileLimit = 50000

// safeMarkers are name fragments of build/tooling caches that are always safe
// to clear. Matched case-insensitively against the subdirectory name.
var safeMarkers = []string{
	"go-build", "gomod", "npm", "_cacache", "yarn", "pnpm", "pip", "cocoapods",
	"homebrew", "node-gyp", "ms-playwright", "puppeteer", "electron", "esbuild",
	"typescript", "carthage", "deno", "gradle", "cargo",
}

// riskyNameFragments identify a file or directory name that suggests real data
// rather than a cache. Matched case-insensitively.
var riskyNameFragments = []string{
	"cookie", "credential", "token", "account", "session", "keychain",
	".sqlite", ".db", ".realm", "leveldb", "indexeddb",
}

// Entry is one cache subdirectory's triage result.
type Entry struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Class     Class  `json:"class"`
	Reason    string `json:"reason,omitempty"`
}

// Report is the triage of every subdirectory of a cache root.
type Report struct {
	Root             string  `json:"root"`
	Entries          []Entry `json:"entries"`
	TotalBytes       int64   `json:"total_bytes"`
	ReclaimableBytes int64   `json:"reclaimable_bytes"`
	RiskyBytes       int64   `json:"risky_bytes"`
}

// Deps injects filesystem access so triage can be tested without real caches.
type Deps struct {
	// Subdirs returns the immediate child directory names of root.
	Subdirs func(root string) ([]string, error)
	// Scan invokes fn for each file under dir (bounded) with its base name and
	// size, so triage can sum size and detect risky markers.
	Scan func(dir string, fn func(name string, size int64)) error
}

// DefaultDeps reads from the real filesystem.
func DefaultDeps() Deps {
	return Deps{
		Subdirs: func(root string) ([]string, error) {
			des, err := os.ReadDir(root)
			if err != nil {
				return nil, err
			}
			var out []string
			for _, de := range des {
				if de.IsDir() {
					out = append(out, de.Name())
				}
			}
			return out, nil
		},
		Scan: func(dir string, fn func(name string, size int64)) error {
			count := 0
			return filepath.WalkDir(dir, func(_ string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || d.IsDir() {
					return nil //nolint:nilerr // tolerate unreadable entries; keep scanning
				}
				count++
				if count > scanFileLimit {
					return filepath.SkipAll
				}
				if info, err := d.Info(); err == nil {
					fn(d.Name(), info.Size())
				}
				return nil
			})
		},
	}
}

// Triage classifies every subdirectory of root and totals the reclaimable and
// risky bytes.
func Triage(root string, deps Deps) (Report, error) {
	subs, err := deps.Subdirs(root)
	if err != nil {
		return Report{Root: root}, err
	}
	rep := Report{Root: root}
	for _, name := range subs {
		dir := filepath.Join(root, name)
		var size int64
		var markers []string
		_ = deps.Scan(dir, func(fileName string, sz int64) {
			size += sz
			if m := riskyMarker(fileName); m != "" && len(markers) < 5 {
				markers = append(markers, m)
			}
		})
		class, reason := classify(name, markers)
		rep.Entries = append(rep.Entries, Entry{Path: dir, Name: name, SizeBytes: size, Class: class, Reason: reason})
		rep.TotalBytes += size
		switch class {
		case ClassRisky:
			rep.RiskyBytes += size
		default:
			rep.ReclaimableBytes += size
		}
	}
	sort.SliceStable(rep.Entries, func(i, j int) bool {
		return rep.Entries[i].SizeBytes > rep.Entries[j].SizeBytes
	})
	return rep, nil
}

// classify decides a subdirectory's class from its name and any risky markers
// found in its subtree. Risky data wins over a safe-looking name.
func classify(name string, markers []string) (Class, string) {
	if len(markers) > 0 {
		return ClassRisky, "contains possible non-cache data: " + strings.Join(markers, ", ")
	}
	lname := strings.ToLower(name)
	for _, m := range safeMarkers {
		if strings.Contains(lname, m) {
			return ClassSafe, "build/tooling cache (" + m + ")"
		}
	}
	return ClassRegenerable, "app runtime cache"
}

// riskyMarker returns the risky fragment a file name matches, or "".
func riskyMarker(name string) string {
	lname := strings.ToLower(name)
	for _, frag := range riskyNameFragments {
		if strings.Contains(lname, frag) {
			return frag
		}
	}
	return ""
}
