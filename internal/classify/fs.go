package classify

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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

// fileID is the part of a stat result that changes whenever a file is
// replaced or rewritten, even when size and mtime are preserved (cp -p,
// rsync, tar, touch -r): the inode, and the ctime only the kernel sets.
type fileID struct {
	dev, ino uint64
	ctime    int64
}

// identity reads dev, inode and ctime from fi.Sys(). Darwin's Stat_t names
// ctime Ctimespec and Linux's names it Ctim, and the integer widths differ;
// reading by field name keeps one pure-Go implementation for both.
func identity(fi fs.FileInfo) fileID {
	v := reflect.ValueOf(fi.Sys())
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return fileID{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fileID{}
	}
	id := fileID{dev: uintField(v, "Dev"), ino: uintField(v, "Ino")}
	for _, name := range []string{"Ctimespec", "Ctim"} {
		if ts := v.FieldByName(name); ts.IsValid() && ts.Kind() == reflect.Struct {
			id.ctime = intField(ts, "Sec")*1e9 + intField(ts, "Nsec")
			break
		}
	}
	return id
}

func uintField(v reflect.Value, name string) uint64 {
	f := v.FieldByName(name)
	switch {
	case !f.IsValid():
		return 0
	case f.CanUint():
		return f.Uint()
	case f.CanInt():
		// #nosec G115 -- device numbers are opaque identity bits, not quantities
		return uint64(f.Int())
	}
	return 0
}

func intField(v reflect.Value, name string) int64 {
	f := v.FieldByName(name)
	switch {
	case !f.IsValid():
		return 0
	case f.CanInt():
		return f.Int()
	case f.CanUint():
		// #nosec G115 -- timespec fields are within int64 range
		return int64(f.Uint())
	}
	return 0
}
