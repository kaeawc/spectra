//go:build !darwin

package detect

// treeBulk has no bulk directory reader off Darwin; walks use ReadDir +
// lstat.
type treeBulk struct{}

func newTreeBulk(bool) treeBulk { return treeBulk{} }

func (treeBulk) release() {}

func (treeBulk) readDir(_ string, stack []string, _ *statBuf, _ treeVisit, _ *treeListing) ([]string, bool) {
	return stack, false
}
