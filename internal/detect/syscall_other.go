//go:build !darwin

package detect

import "os"

func diskBytes(fi os.FileInfo) int64 { return fi.Size() }

// statBuf matches the darwin signature; unused off darwin.
type statBuf struct{}

// lstatRegular reports whether path is a regular file and its sizes.
func lstatRegular(path string, _ *statBuf) (regular bool, actual, apparent int64, ok bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, 0, 0, false
	}
	return fi.Mode().IsRegular(), diskBytes(fi), fi.Size(), true
}
