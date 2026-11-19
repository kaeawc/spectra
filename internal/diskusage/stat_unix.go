//go:build darwin || linux

package diskusage

import (
	"os"
	"syscall"
)

// statOf extracts allocation and identity from fi. Allocation uses
// Stat_t.Blocks (512-byte units) so sparse files report real usage.
func statOf(fi os.FileInfo) fileStat {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fileStat{
			alloc: st.Blocks * 512,
			dev:   uint64(st.Dev), //nolint:unconvert // width differs per OS
			ino:   uint64(st.Ino), //nolint:unconvert
			nlink: uint64(st.Nlink),
			ok:    true,
		}
	}
	return fileStat{alloc: fi.Size()}
}
