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
	// SameFile reports whether a and b (from Stat) are the same device+inode.
	SameFile(a, b os.FileInfo) bool
}

// OSFS is the real filesystem.
type OSFS struct{}

func (OSFS) Stat(name string) (os.FileInfo, error)    { return os.Stat(name) }
func (OSFS) EvalSymlinks(name string) (string, error) { return filepath.EvalSymlinks(name) }

func (OSFS) SameFile(a, b os.FileInfo) bool             { return os.SameFile(a, b) }
func (OSFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }
func (OSFS) Readlink(name string) (string, error)       { return os.Readlink(name) }
func (OSFS) ReadFile(name string) ([]byte, error)       { return os.ReadFile(name) }

// pathMatcher decides whether a /proc link refers to the queried path.
type pathMatcher struct {
	fsys  FS
	paths []string
	ref   os.FileInfo     // identity of the queried path, nil if unknown
	socks map[string]bool // "socket:[inode]" links of unix sockets bound to paths
}

func newPathMatcher(fsys FS, procfs string, paths []string) pathMatcher {
	m := pathMatcher{fsys: fsys, paths: paths, socks: unixSocketLinks(fsys, procfs, paths)}
	if len(paths) > 0 {
		if fi, err := fsys.Stat(paths[0]); err == nil {
			m.ref = fi
		}
	}
	return m
}

// unixSocketLinks resolves bound socket paths through /proc/net/unix
// (Num RefCount Protocol Flags Type St Inode Path) because an fd on a socket
// links to socket:[inode], never to its pathname.
func unixSocketLinks(fsys FS, procfs string, paths []string) map[string]bool {
	b, err := fsys.ReadFile(filepath.Join(procfs, "net", "unix"))
	if err != nil {
		return nil
	}
	var out map[string]bool
	for _, line := range strings.Split(string(b), "\n") {
		inode, path, ok := parseUnixLine(line)
		if !ok || !slices.Contains(paths, path) {
			continue
		}
		if out == nil {
			out = map[string]bool{}
		}
		out["socket:["+inode+"]"] = true
	}
	return out
}

// parseUnixLine splits a /proc/net/unix row into its inode and bound path.
// The first seven columns never contain spaces; everything after the single
// separator is the path verbatim, which may itself contain spaces.
func parseUnixLine(line string) (inode, path string, ok bool) {
	rest := strings.TrimRight(line, "\r\n")
	var cols [7]string
	for i := range cols {
		rest = strings.TrimLeft(rest, " ")
		end := strings.IndexByte(rest, ' ')
		if end < 0 {
			return "", "", false
		}
		cols[i], rest = rest[:end], rest[end+1:]
	}
	if rest == "" {
		return "", "", false
	}
	return cols[6], rest, true
}

func (m pathMatcher) matches(link string) bool {
	ok, _ := m.matchesLink(link)
	return ok
}

// matchesLink compares by device+inode when the link target is a file path,
// so a descriptor on an unlinked file is not mistaken for a new file created
// at the same path and descriptors opened via another hard link still match.
// denied reports a permission failure reading the link; a vanished link (the
// fd closed mid-scan) is not a denial.
func (m pathMatcher) matchesLink(link string) (matched, denied bool) {
	t, err := m.fsys.Readlink(link)
	if err != nil {
		return false, errors.Is(err, fs.ErrPermission)
	}
	return m.matchesTarget(link, t), false
}

func (m pathMatcher) matchesTarget(link, t string) bool {
	if m.socks[t] {
		return true
	}
	name, deleted := strings.CutSuffix(t, " (deleted)")
	if !strings.HasPrefix(name, "/") {
		return false
	}
	if m.ref != nil {
		if fi, err := m.fsys.Stat(link); err == nil && m.fsys.SameFile(m.ref, fi) {
			return true
		}
	}
	return !deleted && slices.Contains(m.paths, name)
}

// procPathHolders scans procfs for processes holding path as an fd, cwd or
// executable. denied is true when some process could not be inspected.
func procPathHolders(fsys FS, procfs string, path []string) (holders []Holder, denied bool) {
	entries, err := fsys.ReadDir(procfs)
	if err != nil {
		return nil, errors.Is(err, fs.ErrPermission)
	}
	m := newPathMatcher(fsys, procfs, path)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		hs, d := procPidHolders(m, filepath.Join(procfs, e.Name()), pid)
		holders = append(holders, hs...)
		denied = denied || d
	}
	return holders, denied
}

func procPidHolders(m pathMatcher, dir string, pid int) (holders []Holder, denied bool) {
	if m.matches(filepath.Join(dir, "cwd")) {
		holders = append(holders, Holder{PID: pid, Hold: HoldCwd})
	}
	if m.matches(filepath.Join(dir, "exe")) {
		holders = append(holders, Holder{PID: pid, Hold: HoldTxt, Detail: "exe"})
	}
	fdDir := filepath.Join(dir, "fd")
	fds, err := m.fsys.ReadDir(fdDir)
	if err != nil {
		return holders, errors.Is(err, fs.ErrPermission)
	}
	for _, fd := range fds {
		ok, d := m.matchesLink(filepath.Join(fdDir, fd.Name()))
		denied = denied || d
		if ok {
			holders = append(holders, Holder{PID: pid, Hold: HoldFD, Detail: "fd " + fd.Name(), Mode: fdMode(m.fsys, dir, fd.Name())})
		}
	}
	return holders, denied
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
