// Package heapdump locates .hprof files a JVM's -XX:+HeapDumpOnOutOfMemoryError
// would have written: inside -XX:HeapDumpPath (a file or a directory) or, when
// that flag is absent, the JVM's working directory under the default
// java_pid<PID>.hprof name. A dump on disk is strong evidence an OOM already
// occurred, and it is directly analyzable with `spectra jvm heap-hprof`.
//
// Like internal/oom, the package is free of snapshot/JVM dependencies; all
// filesystem access goes through the FS seam.
package heapdump

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Discovery bounds: a HeapDumpPath like /tmp can hold many entries, so read a
// bounded prefix of each directory and keep only the newest dumps.
const (
	MaxDirEntries = 1000
	MaxDumps      = 10
)

// FS is the filesystem seam used by Find.
type FS interface {
	Stat(path string) (fs.FileInfo, error)
	// ReadDir returns at most n entries of the directory at path (n <= 0
	// means all).
	ReadDir(path string, n int) ([]fs.DirEntry, error)
}

// OSFS is the live-filesystem FS.
type OSFS struct{}

// Stat implements FS.
func (OSFS) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

// ReadDir implements FS.
func (OSFS) ReadDir(path string, n int) ([]fs.DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(n)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return entries, err
}

// Source records where a dump was found.
type Source string

const (
	SourceHeapDumpPath Source = "heap_dump_path"
	SourceWorkingDir   Source = "working_dir"
)

// Dump is one .hprof file found on disk.
type Dump struct {
	Path      string    `json:"path"`
	SizeBytes int64     `json:"size_bytes"`
	ModTime   time.Time `json:"mod_time"`
	Source    Source    `json:"source"`
	// DumpPID is the PID encoded in a java_pid<PID>.hprof name; 0 when the
	// name does not follow the JVM's default pattern.
	DumpPID int `json:"dump_pid,omitempty"`
}

// Request describes one JVM whose heap dumps should be located.
type Request struct {
	PID          int
	HeapDumpPath string // raw -XX:HeapDumpPath value; may contain %p
	WorkingDir   string
}

const hprofExt = ".hprof"

// HeapDumpPathFromArgs returns the last -XX:HeapDumpPath value in a VM args
// string, or "" when absent. The JVM honours the last occurrence.
func HeapDumpPathFromArgs(vmArgs string) string {
	const prefix = "-XX:HeapDumpPath="
	path := ""
	for _, tok := range strings.Fields(vmArgs) {
		if strings.HasPrefix(tok, prefix) {
			path = strings.Trim(tok[len(prefix):], `"'`)
		}
	}
	return path
}

// PIDFromName parses the PID out of the JVM's default dump name,
// java_pid<PID>.hprof.
func PIDFromName(name string) (int, bool) {
	const prefix = "java_pid"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, hprofExt) {
		return 0, false
	}
	pid, err := strconv.Atoi(name[len(prefix) : len(name)-len(hprofExt)])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// Find returns the .hprof files attributable to the JVM described by req,
// newest first and capped at MaxDumps. A configured HeapDumpPath takes
// precedence: a directory yields every .hprof inside it, a file path yields
// that file. Without one, the working directory is scanned for
// java_pid<PID>.hprof names only, since arbitrary .hprof files there may be
// unrelated captures. I/O errors are absorbed and yield no dumps.
func Find(fsys FS, req Request) []Dump {
	if req.HeapDumpPath == "" {
		if req.WorkingDir == "" {
			return nil
		}
		return newest(scanDir(fsys, req.WorkingDir, SourceWorkingDir, false))
	}
	path := resolvePath(req)
	if path == "" {
		return nil
	}
	fi, err := fsys.Stat(path)
	if err != nil {
		return nil
	}
	if fi.IsDir() {
		return newest(scanDir(fsys, path, SourceHeapDumpPath, true))
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	return []Dump{newDump(path, fi, SourceHeapDumpPath)}
}

// resolvePath expands %p and anchors a relative HeapDumpPath at the working
// directory, as the JVM does. Returns "" when a relative path has no anchor.
func resolvePath(req Request) string {
	path := strings.ReplaceAll(req.HeapDumpPath, "%p", strconv.Itoa(req.PID))
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if req.WorkingDir == "" {
		return ""
	}
	return filepath.Join(req.WorkingDir, path)
}

func scanDir(fsys FS, dir string, src Source, anyName bool) []Dump {
	entries, err := fsys.ReadDir(dir, MaxDirEntries)
	if err != nil && len(entries) == 0 {
		return nil
	}
	var dumps []Dump
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, hprofExt) {
			continue
		}
		if _, ok := PIDFromName(name); !ok && !anyName {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		dumps = append(dumps, newDump(filepath.Join(dir, name), fi, src))
	}
	return dumps
}

func newDump(path string, fi fs.FileInfo, src Source) Dump {
	d := Dump{Path: path, SizeBytes: fi.Size(), ModTime: fi.ModTime(), Source: src}
	if pid, ok := PIDFromName(filepath.Base(path)); ok {
		d.DumpPID = pid
	}
	return d
}

func newest(dumps []Dump) []Dump {
	sort.SliceStable(dumps, func(i, j int) bool {
		if !dumps[i].ModTime.Equal(dumps[j].ModTime) {
			return dumps[i].ModTime.After(dumps[j].ModTime)
		}
		return dumps[i].Path < dumps[j].Path
	})
	if len(dumps) > MaxDumps {
		dumps = dumps[:MaxDumps]
	}
	return dumps
}
