// Package leveldbcheck inspects the structural health of LevelDB stores as used
// by Chromium/Electron for IndexedDB, Local Storage, and Session Storage. It
// reads only the on-disk file layout — never decoding tables or opening the
// store for writing — so it is safe to run against a store a browser is using.
package leveldbcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// compactionBacklogTables flags a store carrying an unusual number of sorted
// table files, which points at a compaction backlog.
const compactionBacklogTables = 500

// logBloatThreshold flags an oversized write-ahead log (100 MiB), which points
// at an unclean shutdown or a large volume of un-compacted writes.
const logBloatThreshold = 100 << 20

// Entry is one directory entry with the fields leveldbcheck needs.
type Entry struct {
	Name  string
	Size  int64
	IsDir bool
}

// Deps injects filesystem access so inspection can run against synthetic store
// layouts in tests.
type Deps struct {
	ReadDir  func(string) ([]Entry, error)
	ReadFile func(string) ([]byte, error)
}

// DefaultDeps reads from the real filesystem.
func DefaultDeps() Deps {
	return Deps{
		ReadDir: func(dir string) ([]Entry, error) {
			des, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			out := make([]Entry, 0, len(des))
			for _, de := range des {
				e := Entry{Name: de.Name(), IsDir: de.IsDir()}
				if info, err := de.Info(); err == nil {
					e.Size = info.Size()
				}
				out = append(out, e)
			}
			return out, nil
		},
		ReadFile: os.ReadFile,
	}
}

// Store is the structural health of one LevelDB store.
type Store struct {
	Path         string   `json:"path"`
	TableFiles   int      `json:"table_files"`
	TableBytes   int64    `json:"table_bytes"`
	LogFiles     int      `json:"log_files"`
	LogBytes     int64    `json:"log_bytes"`
	ManifestFile string   `json:"manifest_file,omitempty"`
	ManifestOK   bool     `json:"manifest_ok"`
	TotalBytes   int64    `json:"total_bytes"`
	Problems     []string `json:"problems,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Report is the combined result of inspecting a set of stores.
type Report struct {
	Stores   []Store `json:"stores"`
	Scanned  int     `json:"scanned"`
	Problems int     `json:"problems"`
}

// Check inspects the store at each path and returns their combined health.
func Check(paths []string, deps Deps) Report {
	rep := Report{}
	for _, p := range paths {
		s := Inspect(p, deps)
		if s.Error != "" || len(s.Problems) > 0 {
			rep.Problems++
		}
		rep.Stores = append(rep.Stores, s)
	}
	rep.Scanned = len(rep.Stores)
	return rep
}

// Inspect reports the structural health of a single LevelDB store directory.
func Inspect(dir string, deps Deps) Store {
	s := Store{Path: dir}
	entries, err := deps.ReadDir(dir)
	if err != nil {
		s.Error = fmt.Errorf("read %s: %w", dir, err).Error()
		return s
	}

	names := make(map[string]bool, len(entries))
	hasCurrent := false
	var largestLog int64
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		names[e.Name] = true
		s.TotalBytes += e.Size
		switch {
		case e.Name == "CURRENT":
			hasCurrent = true
		case strings.HasSuffix(e.Name, ".ldb"), strings.HasSuffix(e.Name, ".sst"):
			s.TableFiles++
			s.TableBytes += e.Size
		case strings.HasSuffix(e.Name, ".log"):
			s.LogFiles++
			s.LogBytes += e.Size
			if e.Size > largestLog {
				largestLog = e.Size
			}
		}
	}

	s.resolveManifest(dir, hasCurrent, names, deps)
	if s.TableFiles > compactionBacklogTables {
		s.Problems = append(s.Problems, fmt.Sprintf("compaction backlog: %d table files", s.TableFiles))
	}
	// Flag on the largest single log, not the aggregate: several normal logs can
	// sum past the threshold without any one being oversized.
	if largestLog > logBloatThreshold {
		s.Problems = append(s.Problems, fmt.Sprintf("write-ahead log bloat: a log file is %d bytes (%d total across %d log(s))", largestLog, s.LogBytes, s.LogFiles))
	}
	return s
}

// resolveManifest records whether CURRENT names an existing manifest file.
func (s *Store) resolveManifest(dir string, hasCurrent bool, names map[string]bool, deps Deps) {
	if !hasCurrent {
		s.Problems = append(s.Problems, "no CURRENT file (not an initialized LevelDB store)")
		return
	}
	raw, err := deps.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		s.Problems = append(s.Problems, "unreadable CURRENT file")
		return
	}
	name := strings.TrimSpace(string(raw))
	s.ManifestFile = name
	if name == "" {
		s.Problems = append(s.Problems, "empty CURRENT file")
		return
	}
	if !strings.HasPrefix(name, "MANIFEST-") {
		s.Problems = append(s.Problems, "CURRENT does not name a manifest: "+name)
		return
	}
	if names[name] {
		s.ManifestOK = true
		return
	}
	s.Problems = append(s.Problems, "CURRENT points to missing manifest "+name)
}

// Discover walks root and returns the directories that are LevelDB stores (a
// directory containing a CURRENT file). Symlinked directories are not followed.
func Discover(root string, deps Deps) []string {
	var stores []string
	var walk func(string)
	walk = func(dir string) {
		entries, err := deps.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir && e.Name == "CURRENT" {
				stores = append(stores, dir)
				break
			}
		}
		for _, e := range entries {
			if e.IsDir {
				walk(filepath.Join(dir, e.Name))
			}
		}
	}
	walk(root)
	sort.Strings(stores)
	return stores
}
