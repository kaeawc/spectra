//go:build darwin || linux

package storagestate

import (
	"os"
	"syscall"
)

// diskBytes returns the actual on-disk allocation for fi using
// Stat_t.Blocks (512-byte units) so sparse files (Docker thin volumes)
// report actual usage, not apparent size.
func diskBytes(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

// fileID identifies an inode across the whole host.
type fileID struct {
	dev uint64
	ino uint64
}

// hardlinkID returns fi's inode identity when it has more than one hard
// link, so the walker can count it once. Single-link files need no
// bookkeeping.
func hardlinkID(fi os.FileInfo) (fileID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink <= 1 {
		return fileID{}, false
	}
	// dev_t is signed on Darwin; it is an opaque identifier, so
	// reinterpreting the bits cannot alias two devices.
	return fileID{dev: uint64(st.Dev), ino: st.Ino}, true //nolint:gosec,unconvert // dev_t width/sign differs per OS
}
