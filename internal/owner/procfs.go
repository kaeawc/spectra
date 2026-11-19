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
	// rel holds "socket:[inode]" links of sockets bound to a relative name
	// whose base name equals the queried one, mapped to that name.
	rel map[string]string
	dir string // /proc/<pid> of the process being scanned
}

func newPathMatcher(fsys FS, procfs string, paths []string) pathMatcher {
	m := pathMatcher{fsys: fsys, paths: paths}
	m.socks, m.rel = unixSocketLinks(fsys, procfs, paths)
	if len(paths) > 0 {
		if fi, err := fsys.Stat(paths[0]); err == nil {
			m.ref = fi
		}
	}
	return m
}

// unixSocketLinks resolves bound socket paths through /proc/net/unix
// (Num RefCount Protocol Flags Type St Inode Path) because an fd on a socket
// links to socket:[inode], never to its pathname. A bind("name") socket is
// listed with its relative name, so those with a matching base name are
// returned separately as candidates for a per-process working-directory check.
func unixSocketLinks(fsys FS, procfs string, paths []string) (abs map[string]bool, rel map[string]string) {
	b, err := fsys.ReadFile(filepath.Join(procfs, "net", "unix"))
	if err != nil {
		return nil, nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		inode, path, ok := parseUnixLine(line)
		if !ok {
			continue
		}
		link := "socket:[" + inode + "]"
		switch {
		case slices.Contains(paths, path):
			if abs == nil {
				abs = map[string]bool{}
			}
			abs[link] = true
		case isRelativeSocketName(path) && baseMatches(paths, path):
			if rel == nil {
				rel = map[string]string{}
			}
			rel[link] = path
		}
	}
	return abs, rel
}

func isRelativeSocketName(p string) bool {
	return !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "@")
}

func baseMatches(paths []string, name string) bool {
	return slices.ContainsFunc(paths, func(p string) bool { return filepath.Base(p) == filepath.Base(name) })
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
	if name, ok := m.rel[t]; ok {
		return m.relativeSocketMatches(name)
	}
	return m.matchesTarget(link, t), false
}

// relativeSocketMatches resolves a relative bound name against the holder's
// current directory. A process that changed directory after bind cannot be
// confirmed this way and is not reported.
func (m pathMatcher) relativeSocketMatches(name string) (matched, denied bool) {
	cwd, err := m.fsys.Readlink(filepath.Join(m.dir, "cwd"))
	if err != nil {
		return false, errors.Is(err, fs.ErrPermission)
	}
	if strings.HasSuffix(cwd, " (deleted)") {
		return false, false
	}
	// Not filepath.Join: lexical cleaning would fold "link/.." away, but the
	// kernel resolved the symlink before applying "..".
	abs := cwd + "/" + name
	if !hasDotDot(name) && slices.Contains(m.paths, filepath.Clean(abs)) {
		return true, false
	}
	if m.ref != nil {
		if fi, err := m.fsys.Stat(abs); err == nil && fi.Mode()&os.ModeSocket != 0 && m.fsys.SameFile(m.ref, fi) {
			return true, false
		}
	}
	return false, false
}

func hasDotDot(name string) bool {
	return slices.Contains(strings.Split(name, "/"), "..")
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
	m.dir = dir
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
