//go:build !darwin && !linux

package storagestate

import "os"

func diskBytes(fi os.FileInfo) int64 { return fi.Size() }

type fileID struct{}

// hardlinkID reports no identity where Stat_t is unavailable, so every
// link is counted.
func hardlinkID(os.FileInfo) (fileID, bool) { return fileID{}, false }
