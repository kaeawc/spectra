//go:build !darwin

package detect

import "os"

func diskBytes(fi os.FileInfo) int64 { return fi.Size() }
