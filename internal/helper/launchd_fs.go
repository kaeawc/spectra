package helper

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// FileMeta is what the launchd checks need from lstat or fstat.
type FileMeta struct {
	Mode    fs.FileMode
	UID     uint32
	GID     uint32
	Dev     uint64
	Ino     uint64
	Size    int64
	ModTime time.Time
}

// LaunchdFS is the filesystem seam for the launchd methods. Every read the
// checks make goes through it, so tests run against a fake tree with
// owners, modes and inodes.
type LaunchdFS interface {
	// Lstat describes path without following a final symlink.
	Lstat(path string) (FileMeta, error)
	// ReadDir lists the names in dir.
	ReadDir(dir string) ([]string, error)
	// OpenNoFollow opens path read-only with O_NOFOLLOW and returns the
	// descriptor's fstat.
	OpenNoFollow(path string) (io.ReadCloser, FileMeta, error)
	// EvalSymlinks resolves every symlink in path.
	EvalSymlinks(path string) (string, error)
}

// osLaunchdFS is the real filesystem.
type osLaunchdFS struct{}

func (osLaunchdFS) Lstat(path string) (FileMeta, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return FileMeta{}, err
	}
	return metaOf(fi)
}

func (osLaunchdFS) ReadDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

func (osLaunchdFS) OpenNoFollow(path string) (io.ReadCloser, FileMeta, error) {
	// O_NONBLOCK: a FIFO planted where a plist is expected must not hang
	// the helper; regular files ignore the flag.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- fixed system paths, verified by the caller
	if err != nil {
		return nil, FileMeta{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, FileMeta{}, err
	}
	meta, err := metaOf(fi)
	if err != nil {
		_ = f.Close()
		return nil, FileMeta{}, err
	}
	return f, meta, nil
}

func (osLaunchdFS) EvalSymlinks(path string) (string, error) { return filepath.EvalSymlinks(path) }

func metaOf(fi fs.FileInfo) (FileMeta, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileMeta{}, errors.New("no stat_t for file")
	}
	return FileMeta{
		Mode: fi.Mode(), UID: st.Uid, GID: st.Gid,
		Dev:  devNum(st.Dev),
		Ino:  st.Ino,
		Size: fi.Size(), ModTime: fi.ModTime(),
	}, nil
}

// errTooLarge marks a file over the read cap.
var errTooLarge = errors.New("file too large")

// readNoFollow opens path with O_NOFOLLOW, requires the descriptor to be
// the file lstat saw (same device, inode, size and mtime), and reads at most
// limit bytes.
func readNoFollow(f LaunchdFS, path string, lst FileMeta, limit int64) ([]byte, FileMeta, error) {
	rc, meta, err := f.OpenNoFollow(path)
	if err != nil {
		return nil, FileMeta{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = rc.Close() }()
	if !sameFile(lst, meta) {
		return nil, meta, fmt.Errorf("%s changed between lstat and open", path)
	}
	if meta.Size > limit {
		return nil, meta, fmt.Errorf("%s: %w (%d bytes, limit %d)", path, errTooLarge, meta.Size, limit)
	}
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, meta, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit || int64(len(data)) != meta.Size {
		return nil, meta, fmt.Errorf("%s changed while it was read", path)
	}
	return data, meta, nil
}

func sameFile(a, b FileMeta) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.ModTime.Equal(b.ModTime) && a.Mode.Type() == b.Mode.Type()
}

// writableByOthers reports group or world write permission.
func writableByOthers(m FileMeta) bool { return m.Mode.Perm()&0o022 != 0 }

// rootDir checks that path is a real directory (not a symlink) owned by
// root and not writable by group or others.
func rootDir(f LaunchdFS, path string) (FileMeta, error) {
	m, err := f.Lstat(path)
	if err != nil {
		return m, err
	}
	switch {
	case m.Mode&fs.ModeSymlink != 0:
		return m, fmt.Errorf("%s is a symbolic link", path)
	case !m.Mode.IsDir():
		return m, fmt.Errorf("%s is not a directory", path)
	case m.UID != 0:
		return m, fmt.Errorf("%s is owned by uid %d, not root", path, m.UID)
	case writableByOthers(m):
		return m, fmt.Errorf("%s is writable by group or others (mode %s)", path, m.Mode.Perm())
	}
	return m, nil
}

// devNum widens a device number: dev_t is int32 on darwin and uint64 on
// linux, so a plain conversion is redundant on one platform. Device numbers
// are only compared for equality, so the bit pattern is what matters.
func devNum[T ~int32 | ~uint64](d T) uint64 {
	return uint64(d) // #nosec G115 -- bit pattern only, compared for equality
}
