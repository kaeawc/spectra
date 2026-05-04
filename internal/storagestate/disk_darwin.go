//go:build darwin

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
