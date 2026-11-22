package classify

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ReadAtCloser is an opened executable: debug/macho and debug/elf read
// through io.ReaderAt, so no whole-file read is needed.
type ReadAtCloser interface {
	io.ReaderAt
	io.Closer
}

// FileSystem is the filesystem seam: every file the classifier touches goes
// through it, so tests run against in-memory fixtures. Open and ReadFile
// must refuse anything but a regular file and must not block on one (a
// FIFO named like an executable or Info.plist would otherwise hang the run).
type FileSystem interface {
	Stat(path string) (fs.FileInfo, error)
	Open(path string) (ReadAtCloser, error)
	// ReadFile reads at most limit bytes and fails when the file is larger.
	ReadFile(path string, limit int64) ([]byte, error)
	// EvalSymlinks resolves a script path an interpreter was given, so a
	// bin/ symlink leads to the npm package that owns the script.
	EvalSymlinks(path string) (string, error)
}

// ErrNotRegular is returned for paths that are not regular files.
var ErrNotRegular = errors.New("not a regular file")

// ErrTooLarge is returned by ReadFile when the file exceeds its limit.
var ErrTooLarge = errors.New("file exceeds read limit")

// OSFileSystem reads the real filesystem.
type OSFileSystem struct{}

// Stat implements FileSystem.
func (OSFileSystem) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

// Open implements FileSystem.
func (OSFileSystem) Open(path string) (ReadAtCloser, error) {
	return openRegular(path)
}

// EvalSymlinks implements FileSystem.
func (OSFileSystem) EvalSymlinks(path string) (string, error) { return filepath.EvalSymlinks(path) }

// ReadFile implements FileSystem.
func (OSFileSystem) ReadFile(path string, limit int64) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w (%d bytes)", path, ErrTooLarge, limit)
	}
	return data, nil
}

// openRegular opens path only if it is a regular file. O_NONBLOCK makes the
// open of a FIFO return at once instead of waiting for a writer; the fstat
// afterwards closes the race with a path swapped in after the first Stat.
func openRegular(path string) (*os.File, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	// #nosec G304 -- the classifier inspects executables and bundles of running processes by design
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err = f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, &fs.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	return f, nil
}
