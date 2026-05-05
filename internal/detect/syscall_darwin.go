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
