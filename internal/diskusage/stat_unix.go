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
			alloc: widen(st.Blocks) * 512,
			dev:   widen(st.Dev),
			ino:   widen(st.Ino),
			nlink: widen(st.Nlink),
			ok:    true,
		}
	}
	return fileStat{alloc: widen(fi.Size())}
}
