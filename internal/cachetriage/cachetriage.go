// Package cachetriage classifies the subdirectories of a cache root (typically
// ~/Library/Caches) as safe, regenerable, or risky to delete, and reports how
// much space could be reclaimed. It only reads the filesystem — it never
// deletes anything.
package cachetriage

import (
	"fmt"
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
	".sqlite", ".db", ".ldb", ".realm", "leveldb", "indexeddb",
}

// Entry is one cache subdirectory's triage result.
type Entry struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	Class      Class  `json:"class"`
	Reason     string `json:"reason,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

// ScanResult reports whether a subdirectory scan saw everything. An incomplete
// or truncated scan cannot prove a directory holds no real data, so triage must
// not classify it as reclaimable.
type ScanResult struct {
	// Truncated is set when the per-directory file limit was reached.
	Truncated bool
	// Incomplete is set when one or more entries could not be read.
	Incomplete bool
}

func (r ScanResult) partial() bool { return r.Truncated || r.Incomplete }

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
	// size, so triage can sum size and detect risky markers. It returns whether
	// the scan was complete; a non-nil error means the subtree could not be
	// walked at all.
	Scan func(dir string, fn func(name string, size int64)) (ScanResult, error)
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
		Scan: func(dir string, fn func(name string, size int64)) (ScanResult, error) {
			var res ScanResult
			count := 0
			err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					// Record that the scan missed something and keep going, rather
					// than aborting; an incomplete scan is held back downstream.
					res.Incomplete = true
					return nil //nolint:nilerr // incompleteness recorded in res; keep scanning
				}
				if d.IsDir() {
					return nil
				}
				count++
				if count > scanFileLimit {
					res.Truncated = true
					return filepath.SkipAll
				}
				info, err := d.Info()
				if err != nil {
					res.Incomplete = true
					return nil //nolint:nilerr // incompleteness recorded in res; keep scanning
				}
				fn(d.Name(), info.Size())
				return nil
			})
			if err != nil {
				return res, fmt.Errorf("scan %s: %w", dir, err)
			}
			return res, nil
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
		res, scanErr := deps.Scan(dir, func(fileName string, sz int64) {
			size += sz
			if m := riskyMarker(fileName); m != "" && len(markers) < 5 {
				markers = append(markers, m)
			}
		})
		incomplete := res.partial() || scanErr != nil
		class, reason := classify(name, markers, incomplete)
		rep.Entries = append(rep.Entries, Entry{Path: dir, Name: name, SizeBytes: size, Class: class, Reason: reason, Incomplete: incomplete})
		rep.TotalBytes += size
		// Only a fully scanned, non-risky directory counts as reclaimable; an
		// incomplete scan (unreadable entries, truncation, or an error) is held
		// back so the user never deletes a directory that was not fully inspected.
		if class == ClassRisky {
			rep.RiskyBytes += size
		} else {
			rep.ReclaimableBytes += size
		}
	}
	sort.SliceStable(rep.Entries, func(i, j int) bool {
		return rep.Entries[i].SizeBytes > rep.Entries[j].SizeBytes
	})
	return rep, nil
}

// classify decides a subdirectory's class from its name, any risky markers found
// in its subtree, and whether the scan was complete. Risky data — and an
// incomplete scan that cannot rule it out — win over a safe-looking name.
func classify(name string, markers []string, incomplete bool) (Class, string) {
	if len(markers) > 0 {
		return ClassRisky, "contains possible non-cache data: " + strings.Join(markers, ", ")
	}
	if incomplete {
		return ClassRisky, "scan incomplete (unreadable or truncated) — held back from reclaimable"
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
