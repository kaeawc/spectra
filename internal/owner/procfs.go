package owner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// FS is the filesystem seam used to walk procfs.
type FS interface {
	ReadDir(name string) ([]os.DirEntry, error)
	Readlink(name string) (string, error)
	ReadFile(name string) ([]byte, error)
	Stat(name string) (os.FileInfo, error)
	EvalSymlinks(name string) (string, error)
}

// OSFS is the real filesystem.
type OSFS struct{}

func (OSFS) Stat(name string) (os.FileInfo, error)    { return os.Stat(name) }
func (OSFS) EvalSymlinks(name string) (string, error) { return filepath.EvalSymlinks(name) }

func (OSFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }
func (OSFS) Readlink(name string) (string, error)       { return os.Readlink(name) }
func (OSFS) ReadFile(name string) ([]byte, error)       { return os.ReadFile(name) }

// procPathHolders scans procfs for processes holding path as an fd, cwd or
// executable. denied is true when some process could not be inspected.
func procPathHolders(fsys FS, procfs string, path []string) (holders []Holder, denied bool) {
	entries, err := fsys.ReadDir(procfs)
	if err != nil {
		return nil, errors.Is(err, fs.ErrPermission)
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		hs, d := procPidHolders(fsys, filepath.Join(procfs, e.Name()), pid, path)
		holders = append(holders, hs...)
		denied = denied || d
	}
	return holders, denied
}

func procPidHolders(fsys FS, dir string, pid int, path []string) (holders []Holder, denied bool) {
	if linkIs(fsys, filepath.Join(dir, "cwd"), path) {
		holders = append(holders, Holder{PID: pid, Hold: HoldCwd})
	}
	if linkIs(fsys, filepath.Join(dir, "exe"), path) {
		holders = append(holders, Holder{PID: pid, Hold: HoldTxt, Detail: "exe"})
	}
	fdDir := filepath.Join(dir, "fd")
	fds, err := fsys.ReadDir(fdDir)
	if err != nil {
		return holders, errors.Is(err, fs.ErrPermission)
	}
	for _, fd := range fds {
		if linkIs(fsys, filepath.Join(fdDir, fd.Name()), path) {
			holders = append(holders, Holder{PID: pid, Hold: HoldFD, Detail: "fd " + fd.Name(), Mode: fdMode(fsys, dir, fd.Name())})
		}
	}
	return holders, false
}

func linkIs(fsys FS, link string, paths []string) bool {
	t, err := fsys.Readlink(link)
	return err == nil && slices.Contains(paths, strings.TrimSuffix(t, " (deleted)"))
}

// fdMode reads the open flags from fdinfo; the low two bits are the access mode.
func fdMode(fsys FS, dir, fd string) string {
	b, err := fsys.ReadFile(filepath.Join(dir, "fdinfo", fd))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		v, ok := strings.CutPrefix(line, "flags:")
		if !ok {
			continue
		}
		flags, err := strconv.ParseInt(strings.TrimSpace(v), 8, 64)
		if err != nil {
			return ""
		}
		return [...]string{"r", "w", "rw"}[min(flags&3, 2)]
	}
	return ""
}
