//go:build darwin

package storagestate

import (
	"encoding/binary"
	"os"

	"github.com/kaeawc/spectra/internal/fsbulk"
	"golang.org/x/sys/unix"
)

const (
	bulkCommonAttrs = unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_NAME | unix.ATTR_CMN_DEVID |
		unix.ATTR_CMN_OBJTYPE | unix.ATTR_CMN_FILEID | unix.ATTR_CMN_ERROR
	bulkFileAttrs = unix.ATTR_FILE_LINKCOUNT | unix.ATTR_FILE_ALLOCSIZE

	vREG = fsbulk.VREG
	vDIR = fsbulk.VDIR

	// Record layout under FSOPT_PACK_INVAL_ATTRS (see fsbulk): the
	// remaining attributes follow ATTR_CMN_ERROR in bit order.
	offReturned   = fsbulk.OffReturned
	offError      = fsbulk.OffError
	offNameRef    = fsbulk.OffFirst  // attrreference_t {int32 offset, u_int32 length}
	offDevID      = offNameRef + 8   // dev_t
	offObjType    = offDevID + 4     // fsobj_type_t
	offFileID     = offObjType + 4   // u_int64_t
	offLinkCount  = offFileID + 8    // u_int32_t (file attrs, non-directories)
	offAllocSize  = offLinkCount + 4 // off_t
	minCommonLen  = offLinkCount
	minFileRecLen = offAllocSize + 8
)

var errBulkMalformed = fsbulk.ErrMalformed

// bulkEntry is one decoded getattrlistbulk record.
type bulkEntry struct {
	// name is the entry name as a string. decodeBulk fills it; the tree
	// walker's decoder leaves it empty and uses raw instead.
	name string
	// raw is the name's bytes, aliasing the result buffer: valid only until
	// the next getattrlistbulk call into that buffer.
	raw     []byte
	isDir   bool
	isReg   bool
	errno   uint32
	dev     uint32
	ino     uint64
	nlink   uint32
	alloc   int64
	hasType bool
	hasSize bool // alloc is valid
	hasLink bool // nlink, dev and ino are valid
}

// bulkScratch is one walker goroutine's reusable bulk-reader state: a
// result buffer and the decoded-entry slice.
type bulkScratch struct {
	bp      *[]byte
	entries []bulkEntry
}

func (s *bulkScratch) buffer() []byte {
	if s.bp == nil {
		s.bp = fsbulk.GetBuf()
	}
	return *s.bp
}

// release returns the buffer to the pool; the scratch stays usable.
func (s *bulkScratch) release() {
	if s.bp != nil {
		fsbulk.PutBuf(s.bp)
		s.bp = nil
	}
	clear(s.entries)
	s.entries = s.entries[:0]
}

// readDirBulk sizes d with getattrlistbulk(2), which returns names, types,
// allocated sizes and inode identity for a whole batch of entries per call
// instead of one lstat each. It reports false, having accounted nothing, when
// the bulk reader cannot handle the directory, so the caller can fall back
// to the lstat path.
func (w *treeWalker) readDirBulk(d walkDir, tracked []int64) (int64, bool) {
	s := &walkScratch{tracked: tracked}
	defer s.bulk.release()
	return w.readDirBulkWith(d, s)
}

// readDirBulkWith is readDirBulk accumulating tracked bytes into s.tracked
// and reusing s's buffers.
func (w *treeWalker) readDirBulkWith(d walkDir, s *walkScratch) (int64, bool) {
	fd, err := fsbulk.OpenDir(d.path)
	if err != nil {
		w.addSkipped()
		return 0, true
	}
	defer unix.Close(fd) //nolint:errcheck // read-only directory descriptor

	buf := s.bulk.buffer()
	var total int64
	for first := true; ; first = false {
		n, err := fsbulk.Read(fd, buf, bulkCommonAttrs, bulkFileAttrs)
		if err == nil && n > 0 {
			s.bulk.entries, err = decodeBulkRecords(buf, n, s.bulk.entries[:0], false)
		}
		if err != nil {
			if first {
				return 0, false
			}
			w.addSkipped()
			break
		}
		if n == 0 {
			break
		}
		total += w.applyBulk(d, s.bulk.entries, s)
	}
	return total, true
}

// applyBulk accounts one decoded batch and queues its subdirectories.
func (w *treeWalker) applyBulk(d walkDir, entries []bulkEntry, s *walkScratch) int64 {
	var total int64
	for i := range entries {
		e := &entries[i]
		switch {
		case e.hasType && e.isDir:
			s.dirs = append(s.dirs, w.child(d, e.raw))
		case e.errno != 0 || !e.hasType || !e.hasSize:
			// The filesystem could not supply what this entry needs;
			// size it the slow way so semantics stay those of lstat.
			fi, err := os.Lstat(joinName(d.path, e.raw))
			switch {
			case err != nil:
			case fi.IsDir():
				s.dirs = append(s.dirs, w.child(d, e.raw))
			default:
				total += w.sizeInfo(fi, d.tracked, s.tracked)
			}
		default:
			linked := e.isReg && e.hasLink && e.nlink > 1
			var id fileID
			if linked {
				// dev_t is signed on Darwin; widen it the way hardlinkID
				// widens Stat_t.Dev so both paths key inodes identically.
				id = fileID{dev: signExtend32(e.dev), ino: e.ino}
			}
			total += w.account(fsbulk.AllocBlocks(e.alloc), id, linked, d.tracked, s.tracked)
		}
	}
	w.pushScratch(s)
	return total
}

// decodeBulk parses n records from buf, appending to out. It never reads
// past buf or a record's own declared length.
func decodeBulk(buf []byte, n int, out []bulkEntry) ([]bulkEntry, error) {
	return decodeBulkRecords(buf, n, out, true)
}

// decodeBulkRecords is decodeBulk with name materialization optional: when
// names is false only raw is set, so sizing files allocates nothing.
func decodeBulkRecords(buf []byte, n int, out []bulkEntry, names bool) ([]bulkEntry, error) {
	off := 0
	for range n {
		rec, err := fsbulk.NextRecord(buf, off, minCommonLen)
		if err != nil {
			return out, err
		}
		e, err := decodeBulkRecord(rec)
		if err != nil {
			return out, err
		}
		if names {
			e.name = string(e.raw)
		}
		out = append(out, e)
		off += len(rec)
	}
	return out, nil
}

func decodeBulkRecord(rec []byte) (bulkEntry, error) {
	ne := binary.NativeEndian
	retCommon := ne.Uint32(rec[offReturned:])
	retFile := ne.Uint32(rec[offReturned+12:])
	if retCommon&unix.ATTR_CMN_NAME == 0 {
		return bulkEntry{}, errBulkMalformed
	}
	raw, err := fsbulk.NameAt(rec, offNameRef)
	if err != nil {
		return bulkEntry{}, err
	}
	e := bulkEntry{raw: raw}
	if retCommon&unix.ATTR_CMN_ERROR != 0 {
		e.errno = ne.Uint32(rec[offError:])
	}
	if retCommon&unix.ATTR_CMN_OBJTYPE != 0 {
		t := ne.Uint32(rec[offObjType:])
		e.hasType, e.isDir, e.isReg = true, t == vDIR, t == vREG
	}
	if e.isDir || len(rec) < minFileRecLen {
		return e, nil
	}
	if retFile&unix.ATTR_FILE_ALLOCSIZE != 0 {
		e.alloc, e.hasSize = fsbulk.OffT(rec, offAllocSize)
	}
	const idBits = unix.ATTR_CMN_DEVID | unix.ATTR_CMN_FILEID
	if retCommon&idBits == idBits && retFile&unix.ATTR_FILE_LINKCOUNT != 0 {
		e.dev = ne.Uint32(rec[offDevID:])
		e.ino = ne.Uint64(rec[offFileID:])
		e.nlink = ne.Uint32(rec[offLinkCount:])
		e.hasLink = true
	}
	return e, nil
}

// signExtend32 widens a signed 32-bit dev_t carried as uint32 the way
// hardlinkID widens Stat_t.Dev, so both walk paths key inodes identically.
func signExtend32(v uint32) uint64 {
	w := uint64(v)
	if v&0x80000000 != 0 {
		w |= 0xFFFFFFFF00000000
	}
	return w
}
