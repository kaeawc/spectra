package uninstall

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/kaeawc/spectra/internal/diskusage"
)

// Meta is the lstat view of a path: it never follows a final symlink.
type Meta struct {
	Mode fs.FileMode
	UID  int64
	GID  int64
}

// IsSymlink reports whether the path itself is a symbolic link.
func (m Meta) IsSymlink() bool { return m.Mode&fs.ModeSymlink != 0 }

// FS is the filesystem seam. Every call is lstat-based so a symlink item is
// treated as the link, never its target.
type FS interface {
	Meta(path string) (Meta, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	ReadFile(path string) ([]byte, error)
	Rename(oldpath, newpath string) error
	// Mkdir creates one directory and fails if anything already exists there.
	Mkdir(path string, perm fs.FileMode) error
	Readlink(path string) (string, error)
	// EvalSymlinks resolves every symbolic link on path.
	EvalSymlinks(path string) (string, error)
	// Size returns allocated bytes: the link itself for a symlink, the
	// whole tree (not following symlinks) for a directory.
	Size(path string) uint64
}

// OSFS is the real filesystem.
type OSFS struct{}

// Meta implements FS.
func (OSFS) Meta(path string) (Meta, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Meta{}, err
	}
	m := Meta{Mode: fi.Mode(), UID: -1, GID: -1}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		m.UID, m.GID = int64(st.Uid), int64(st.Gid)
	}
	return m, nil
}

// ReadDir implements FS.
func (OSFS) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }

// ReadFile implements FS.
func (OSFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(filepath.Clean(path)) }

// Rename implements FS.
func (OSFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// Mkdir implements FS.
func (OSFS) Mkdir(path string, perm fs.FileMode) error { return os.Mkdir(path, perm) }

// Readlink implements FS.
func (OSFS) Readlink(path string) (string, error) { return os.Readlink(path) }

// EvalSymlinks implements FS.
func (OSFS) EvalSymlinks(path string) (string, error) { return filepath.EvalSymlinks(path) }

// Size implements FS.
func (OSFS) Size(path string) uint64 {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if !fi.IsDir() {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Blocks > 0 {
			return uint64(st.Blocks) * 512
		}
		if sz := fi.Size(); fi.Mode().IsRegular() && sz > 0 {
			return uint64(sz)
		}
		return 0
	}
	res, err := diskusage.Walk(context.Background(), path, diskusage.Options{Top: 1})
	if err != nil {
		return 0
	}
	return res.TotalBytes
}

func exists(f FS, path string) bool {
	_, err := f.Meta(path)
	return err == nil
}

func isDir(f FS, path string) bool {
	m, err := f.Meta(path)
	return err == nil && m.Mode.IsDir()
}

// canWrite reports whether uid (with gids) holds write permission on a path
// with meta m, judged from the mode bits.
func canWrite(m Meta, uid int64, gids []int64) bool {
	if uid == 0 {
		return true
	}
	if m.UID == uid {
		return m.Mode.Perm()&0o200 != 0
	}
	for _, g := range gids {
		if g == m.GID {
			return m.Mode.Perm()&0o020 != 0
		}
	}
	return m.Mode.Perm()&0o002 != 0
}

// removable reports whether the current user can move path out of its
// parent: the user owns it, can write the parent, and (for a directory,
// whose ".." entry changes) can write the directory itself. It never tries.
func removable(f FS, path string, m Meta, uid int64, gids []int64) bool {
	if uid == 0 || m.UID != uid {
		return false
	}
	parent, err := f.Meta(filepath.Dir(path))
	if err != nil || !canWrite(parent, uid, gids) {
		return false
	}
	if m.Mode.IsDir() && !canWrite(m, uid, gids) {
		return false
	}
	return true
}
