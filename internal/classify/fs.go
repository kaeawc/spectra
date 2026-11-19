package classify

import (
	"io"
	"io/fs"
	"os"
)

// ReadAtCloser is an opened executable: debug/macho and debug/elf read
// through io.ReaderAt, so no whole-file read is needed.
type ReadAtCloser interface {
	io.ReaderAt
	io.Closer
}

// FileSystem is the filesystem seam: every file the classifier touches goes
// through it, so tests run against in-memory fixtures.
type FileSystem interface {
	Stat(path string) (fs.FileInfo, error)
	Open(path string) (ReadAtCloser, error)
	ReadFile(path string) ([]byte, error)
}

// OSFileSystem reads the real filesystem.
type OSFileSystem struct{}

// Stat implements FileSystem.
func (OSFileSystem) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

// Open implements FileSystem.
func (OSFileSystem) Open(path string) (ReadAtCloser, error) {
	// #nosec G304 -- the classifier inspects executables of running processes by design
	return os.Open(path)
}

// ReadFile implements FileSystem.
func (OSFileSystem) ReadFile(path string) ([]byte, error) {
	// #nosec G304 -- reads Info.plist of bundles that own running processes
	return os.ReadFile(path)
}
