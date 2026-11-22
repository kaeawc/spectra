//go:build !darwin

package storagestate

// bulkScratch holds nothing where there is no bulk reader.
type bulkScratch struct{}

func (*bulkScratch) release() {}

// readDirBulk has no bulk reader off macOS; readDir falls back to lstat.
func (w *treeWalker) readDirBulk(walkDir, []int64) (int64, bool) { return 0, false }

func (w *treeWalker) readDirBulkWith(walkDir, *walkScratch) (int64, bool) { return 0, false }

// readLogDirBulk has no bulk reader off macOS; the log walker falls back
// to ReadDir.
func readLogDirBulk(string, string) ([]string, []LogFile, bool) { return nil, nil, false }
