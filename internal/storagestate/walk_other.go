//go:build !darwin

package storagestate

// readDirBulk has no bulk reader off macOS; readDir falls back to lstat.
func (w *treeWalker) readDirBulk(walkDir, []int64) (int64, bool) { return 0, false }
