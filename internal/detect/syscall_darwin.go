//go:build darwin

package detect

import (
	"os"
	"syscall"
)

// diskBytes returns the actual on-disk allocation for a regular file,
// computed from st_blocks. Falls back to apparent size if the Sys data
// is unavailable for any reason.
func diskBytes(fi os.FileInfo) int64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fi.Size()
	}
	return st.Blocks * 512
}

// statBuf is a caller-owned lstat result reused across a walk.
type statBuf = syscall.Stat_t

// lstatRegular lstats path into st (no FileInfo allocation) and reports
// whether it is a regular file with its actual and apparent sizes. ok is
// false when the lstat failed.
func lstatRegular(path string, st *statBuf) (regular bool, actual, apparent int64, ok bool) {
	if err := syscall.Lstat(path, st); err != nil {
		return false, 0, 0, false
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG, st.Blocks * 512, st.Size, true
}
