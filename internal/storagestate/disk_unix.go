//go:build darwin || linux

package storagestate

import (
	"os"
	"syscall"
)

// diskBytes returns the actual on-disk allocation for fi using
// Stat_t.Blocks (512-byte units) so sparse files (Docker thin volumes)
// report actual usage, not apparent size. Stat_t.Blocks has identical
// semantics on Darwin and Linux.
func diskBytes(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}
