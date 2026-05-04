package fsutil

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path with perm, using tempfile + fsync + rename
// so a concurrent reader never sees a truncated file and a crash mid-write never
// leaves a torn file on disk.
//
// Parent directories must already exist — callers that want MkdirAll should call
// it themselves, to keep this helper one syscall wide.
//
// The tempfile is created in the same directory as path (same filesystem =
// rename is atomic on POSIX) with prefix derived from filepath.Base(path).
// On any error after tempfile creation, the tempfile is removed best-effort.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeAtomic(path, perm, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// WriteFileAtomicStream is like WriteFileAtomic but streams into a caller-provided
// callback. Used for gob/json encoders that want to write directly into the
// tempfile without materialising the full payload.
//
//	err := WriteFileAtomicStream(path, perm, func(w io.Writer) error {
//	    return gob.NewEncoder(w).Encode(v)
//	})
//
// The writer is buffered; flush is handled by the helper. fsync runs after
// flush and before rename.
func WriteFileAtomicStream(path string, perm os.FileMode, write func(io.Writer) error) error {
	return writeAtomic(path, perm, func(f *os.File) error {
		bw := bufio.NewWriter(f)
		if err := write(bw); err != nil {
			return err
		}
		return bw.Flush()
	})
}

// writeAtomic is the shared tempfile + fsync + rename core. payload decides
// how bytes reach the file handle (direct write vs. buffered stream).
func writeAtomic(path string, perm os.FileMode, payload func(*os.File) error) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("write tempfile: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("write tempfile: %w", err)
	}

	if err := payload(tmp); err != nil {
		cleanup()
		return fmt.Errorf("write tempfile: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync tempfile: %w", err)
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close tempfile: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename tempfile: %w", err)
	}

	return nil
}
