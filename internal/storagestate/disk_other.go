//go:build !darwin

package storagestate

import "os"

func diskBytes(fi os.FileInfo) int64 { return fi.Size() }
