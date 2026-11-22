//go:build darwin

package detect

import (
	"syscall"

	"github.com/kaeawc/spectra/internal/fsbulk"
)

// treeBulk lists tree-walk directories with getattrlistbulk, which returns
// each entry's type and both sizes in one batched call instead of one lstat
// per entry. A nil lister disables it.
type treeBulk struct {
	l *fsbulk.Lister
}

func newTreeBulk(lstatOnly bool) treeBulk {
	if lstatOnly {
		return treeBulk{}
	}
	return treeBulk{l: &fsbulk.Lister{}}
}

func (b treeBulk) release() {
	if b.l != nil {
		b.l.Release()
	}
}

// readDir is readTreeDir through the bulk reader. It reports false, having
// visited nothing, when the bulk reader cannot handle dir, so the caller
// falls back to readTreeDir.
func (b treeBulk) readDir(dir string, stack []string, st *statBuf, visit treeVisit) ([]string, bool) {
	if b.l == nil {
		return stack, false
	}
	ok := b.l.ReadDir(dir, func(e *fsbulk.Entry) {
		p := dir + "/" + string(e.Name)
		name := p[len(dir)+1:]
		switch {
		case e.IsDir():
			stack = append(stack, p)
		case e.Complete():
			// ATTR_FILE_ALLOCSIZE is what lstat derives st_blocks from, and
			// ATTR_FILE_DATALENGTH is st_size, so these equal the lstat
			// path's Blocks*512 and Size.
			visit(p, name, e.Type == fsbulk.VREG, fsbulk.AllocBlocks(e.Alloc), e.Size)
		default:
			// The filesystem could not supply what this entry needs; lstat
			// it so semantics stay those of readTreeDir.
			stack = visitLstat(p, name, stack, st, visit)
		}
	})
	return stack, ok
}

// visitLstat sizes one entry the bulk reader could not, as readTreeDir would.
func visitLstat(p, name string, stack []string, st *statBuf, visit treeVisit) []string {
	if err := syscall.Lstat(p, st); err != nil {
		visit(p, name, false, 0, 0)
		return stack
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		return append(stack, p)
	case syscall.S_IFREG:
		visit(p, name, true, st.Blocks*512, st.Size)
	default:
		visit(p, name, false, st.Blocks*512, st.Size)
	}
	return stack
}
