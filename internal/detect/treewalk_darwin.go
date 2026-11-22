//go:build darwin

package detect

import (
	"io/fs"
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
func (b treeBulk) readDir(dir string, stack []string, st *statBuf, visit treeVisit, ls *treeListing) ([]string, bool) {
	if b.l == nil {
		return stack, false
	}
	ok, err := b.l.ReadDirErr(dir, func(e *fsbulk.Entry) {
		p := dir + "/" + string(e.Name)
		name := p[len(dir)+1:]
		mode, known := vnodeMode(e.Type)
		switch {
		case e.IsDir():
			ls.add(p, len(dir), fs.ModeDir, 0)
			stack = append(stack, p)
		case e.Complete() && known:
			// ATTR_FILE_ALLOCSIZE is what lstat derives st_blocks from, and
			// ATTR_FILE_DATALENGTH is st_size, so these equal the lstat
			// path's Blocks*512 and Size.
			ls.add(p, len(dir), mode, e.Size)
			visit(p, name, e.Type == fsbulk.VREG, fsbulk.AllocBlocks(e.Alloc), e.Size)
		default:
			// The filesystem could not supply what this entry needs; lstat
			// it so semantics stay those of readTreeDir.
			stack = visitLstat(p, len(dir), stack, st, visit, ls)
		}
	})
	if ok && err != nil {
		ls.fail()
	}
	return stack, ok
}

// vnodeMode maps an fsobj_type_t to the type bits os.ReadDir reports for the
// matching dirent type. known is false for types ReadDir would lstat.
func vnodeMode(t uint32) (mode fs.FileMode, known bool) {
	switch t {
	case fsbulk.VREG:
		return 0, true
	case fsbulk.VDIR:
		return fs.ModeDir, true
	case fsbulk.VLNK:
		return fs.ModeSymlink, true
	case fsbulk.VBLK:
		return fs.ModeDevice, true
	case fsbulk.VCHR:
		return fs.ModeDevice | fs.ModeCharDevice, true
	case fsbulk.VSOCK:
		return fs.ModeSocket, true
	case fsbulk.VFIFO:
		return fs.ModeNamedPipe, true
	}
	return 0, false
}

// visitLstat sizes one entry the bulk reader could not, as readTreeDir would.
// p's parent directory is p[:dirLen].
func visitLstat(p string, dirLen int, stack []string, st *statBuf, visit treeVisit, ls *treeListing) []string {
	name := p[dirLen+1:]
	if err := syscall.Lstat(p, st); err != nil {
		ls.fail()
		visit(p, name, false, 0, 0)
		return stack
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		ls.add(p, dirLen, fs.ModeDir, 0)
		return append(stack, p)
	case syscall.S_IFREG:
		ls.add(p, dirLen, 0, st.Size)
		visit(p, name, true, st.Blocks*512, st.Size)
	default:
		ls.add(p, dirLen, statTypeMode(st.Mode), st.Size)
		visit(p, name, false, st.Blocks*512, st.Size)
	}
	return stack
}

// statTypeMode maps the S_IFMT bits of a non-directory, non-regular lstat to
// the type bits os.Lstat reports.
func statTypeMode(m uint16) fs.FileMode {
	switch m & syscall.S_IFMT {
	case syscall.S_IFLNK:
		return fs.ModeSymlink
	case syscall.S_IFBLK:
		return fs.ModeDevice
	case syscall.S_IFCHR:
		return fs.ModeDevice | fs.ModeCharDevice
	case syscall.S_IFSOCK:
		return fs.ModeSocket
	case syscall.S_IFIFO:
		return fs.ModeNamedPipe
	}
	return fs.ModeIrregular
}
