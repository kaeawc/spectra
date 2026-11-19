//go:build !darwin && !linux

package diskusage

import "os"

func statOf(fi os.FileInfo) fileStat { return fileStat{alloc: fi.Size()} }
