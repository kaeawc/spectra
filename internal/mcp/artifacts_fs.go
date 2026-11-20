package mcp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// A tool result's resource links are read back by an AI client, so anything
// that could make spectra-mcp serve a file it did not write is an
// exfiltration path. Every directory on the way to an artifact must be a
// real directory the current user owns that nobody else can write, and an
// artifact is opened without following symlinks and checked on the open fd.

const (
	// artifactMaxAge and artifactKindBytes bound what one kind keeps; a save
	// sweeps older and then oldest-first excess artifacts.
	artifactMaxAge    = 7 * 24 * time.Hour
	artifactKindBytes = 512 << 20
	// maxArtifactSweep bounds the entries one sweep reads.
	maxArtifactSweep = 10000
)

var errUnsafeArtifactDir = errors.New("artifacts directory is unsafe")

// ownedByMe reports whether info (from Lstat or Fstat) belongs to the
// current user.
func ownedByMe(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// checkDir requires path to be a real directory the current user owns. With
// create, a missing directory is made 0700. With tighten, an owned directory
// that is group- or world-accessible is chmodded to 0700; without it, a
// group- or world-writable one is refused.
func checkDir(path string, create, tighten bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create %s: %w", path, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() || !ownedByMe(info) {
		return fmt.Errorf("%w: %s is not a directory owned by the current user", errUnsafeArtifactDir, path)
	}
	if tighten && info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- a directory needs x; 0700 is owner-only
			return fmt.Errorf("tighten %s: %w", path, err)
		}
		return nil
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %s is group- or world-writable", errUnsafeArtifactDir, path)
	}
	return nil
}

// artifactKindDir returns the kind's directory after checking ~/.spectra (for
// the default root), the artifacts root and the kind directory itself.
// create makes missing directories (a save); a read never creates.
func (s *Server) artifactKindDir(kind string, create bool) (string, error) {
	if !validArtifactKind(kind) {
		return "", fmt.Errorf("unknown artifact kind %q", kind)
	}
	root, spectraDir, err := s.artifactsRoot()
	if err != nil {
		return "", err
	}
	if spectraDir != "" {
		if err := checkDir(spectraDir, create, false); err != nil {
			return "", err
		}
	}
	if err := checkDir(root, create, true); err != nil {
		return "", err
	}
	dir := filepath.Join(root, kind)
	if err := checkDir(dir, create, false); err != nil {
		return "", err
	}
	return dir, nil
}

// openArtifact opens id inside dir without following a symlink and checks
// the open file: a regular file the current user owns.
func openArtifact(dir, id string) (*os.File, fs.FileInfo, error) {
	if !validArtifactID(id) {
		return nil, nil, errors.New("invalid artifact id")
	}
	f, err := os.OpenFile(filepath.Join(dir, id), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- id is validated and dir checked
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || !ownedByMe(info) {
		f.Close()
		return nil, nil, errors.New("artifact is not a regular file owned by the current user")
	}
	return f, info, nil
}

// readCapped reads at most limit bytes from r; over reports that r held more.
func readCapped(r io.Reader, limit int64) (data []byte, over bool, err error) {
	data, err = io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, true, nil
	}
	return data, false, nil
}

// sweepArtifacts deletes a kind's artifacts older than artifactMaxAge, then
// the oldest until the kind holds at most artifactKindBytes. keep is never
// deleted.
func (s *Server) sweepArtifacts(dir, keep string) {
	f, err := os.Open(dir) // #nosec G304 -- a checked artifacts directory
	if err != nil {
		return
	}
	entries, _ := f.ReadDir(maxArtifactSweep)
	f.Close()
	type item struct {
		name string
		mod  time.Time
		size int64
	}
	var items []item
	var total int64
	now := s.now()
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.Name() == keep || !validArtifactID(e.Name()) || !info.Mode().IsRegular() || !ownedByMe(info) {
			continue
		}
		if now.Sub(info.ModTime()) > artifactMaxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		items = append(items, item{e.Name(), info.ModTime(), info.Size()})
		total += info.Size()
	}
	if keepInfo, err := os.Lstat(filepath.Join(dir, keep)); err == nil {
		total += keepInfo.Size()
	}
	limit := s.artifactKindLimit()
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= limit {
			break
		}
		if os.Remove(filepath.Join(dir, it.name)) == nil {
			total -= it.size
		}
	}
}

func (s *Server) artifactKindLimit() int64 {
	if s.artifactKindBytes > 0 {
		return s.artifactKindBytes
	}
	return artifactKindBytes
}

// newArtifactFile creates a fresh, empty artifact file for a tool to write
// into (O_EXCL, so it is ours and new) and returns its path and id.
func (s *Server) newArtifactFile(kind, prefix, ext string) (path, id string, err error) {
	dir, err := s.artifactKindDir(kind, true)
	if err != nil {
		return "", "", err
	}
	id = s.newArtifactID(prefix, ext)
	path = filepath.Join(dir, id)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- a generated name in a checked directory
	if err != nil {
		return "", "", err
	}
	return path, id, f.Close()
}
