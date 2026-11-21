//go:build darwin

package storagestate

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// bulkBufSize is the getattrlistbulk result buffer per call. Larger buffers
// mean fewer system calls on wide directories.
const bulkBufSize = 128 << 10

var bulkBufPool = sync.Pool{New: func() any {
	b := make([]byte, bulkBufSize)
	return &b
}}

const (
	bulkCommonAttrs = unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_NAME | unix.ATTR_CMN_DEVID |
		unix.ATTR_CMN_OBJTYPE | unix.ATTR_CMN_FILEID | unix.ATTR_CMN_ERROR
	bulkFileAttrs = unix.ATTR_FILE_LINKCOUNT | unix.ATTR_FILE_ALLOCSIZE

	// fsobj_type_t values from <sys/vnode.h>.
	vREG = 1
	vDIR = 2

	// Fixed record layout under FSOPT_PACK_INVAL_ATTRS: every requested
	// attribute is packed, invalid ones zeroed, so offsets do not depend on
	// which attributes the filesystem returned. ATTR_CMN_ERROR is packed
	// directly after ATTR_CMN_RETURNED_ATTRS; the rest follow in bit order.
	offReturned   = 4                // attribute_set_t: common, vol, dir, file, fork
	offError      = offReturned + 20 // u_int32_t
	offNameRef    = offError + 4     // attrreference_t {int32 offset, u_int32 length}
	offDevID      = offNameRef + 8   // dev_t
	offObjType    = offDevID + 4     // fsobj_type_t
	offFileID     = offObjType + 4   // u_int64_t
	offLinkCount  = offFileID + 8    // u_int32_t (file attrs, non-directories)
	offAllocSize  = offLinkCount + 4 // off_t
	minCommonLen  = offLinkCount
	minFileRecLen = offAllocSize + 8
)

var errBulkMalformed = errors.New("getattrlistbulk: malformed record")

// bulkEntry is one decoded getattrlistbulk record.
type bulkEntry struct {
	name    string
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

// readDirBulk sizes d with getattrlistbulk(2), which returns names, types,
// allocated sizes and inode identity for a whole batch of entries per call
// instead of one lstat each. It reports false, having accounted nothing, when
// the bulk reader cannot handle the directory, so the caller can fall back
// to the lstat path.
func (w *treeWalker) readDirBulk(d walkDir, tracked []int64) (int64, bool) {
	fd, err := openDirFD(d.path)
	if err != nil {
		w.addSkipped()
		return 0, true
	}
	defer unix.Close(fd) //nolint:errcheck // read-only directory descriptor

	bp := bulkBufPool.Get().(*[]byte)
	defer bulkBufPool.Put(bp)
	buf := *bp

	var total int64
	var entries []bulkEntry
	for first := true; ; first = false {
		n, err := getattrlistbulk(fd, buf)
		if err == nil && n > 0 {
			entries, err = decodeBulk(buf, n, entries[:0])
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
		total += w.applyBulk(d, entries, tracked)
	}
	return total, true
}

// applyBulk accounts one decoded batch and queues its subdirectories.
func (w *treeWalker) applyBulk(d walkDir, entries []bulkEntry, tracked []int64) int64 {
	var total int64
	var dirs []walkDir
	for i := range entries {
		e := &entries[i]
		switch {
		case e.hasType && e.isDir:
			dirs = append(dirs, w.child(d, e.name))
		case e.errno != 0 || !e.hasType || !e.hasSize:
			// The filesystem could not supply what this entry needs;
			// size it the slow way so semantics stay those of lstat.
			fi, err := os.Lstat(filepath.Join(d.path, e.name))
			switch {
			case err != nil:
			case fi.IsDir():
				dirs = append(dirs, w.child(d, e.name))
			default:
				total += w.sizeInfo(fi, d.tracked, tracked)
			}
		default:
			linked := e.isReg && e.hasLink && e.nlink > 1
			var id fileID
			if linked {
				// dev_t is signed on Darwin; widen it the way hardlinkID
				// widens Stat_t.Dev so both paths key inodes identically.
				id = fileID{dev: signExtend32(e.dev), ino: e.ino}
			}
			total += w.account(allocBlocks(e.alloc), id, linked, d.tracked, tracked)
		}
	}
	w.push(dirs)
	return total
}

// allocBlocks rounds an allocation up to whole 512-byte blocks, matching how
// the kernel derives st_blocks from the same allocation for lstat.
func allocBlocks(alloc int64) int64 {
	return (alloc + 511) / 512 * 512
}

// decodeBulk parses n records from buf, appending to out. It never reads
// past buf or a record's own declared length.
func decodeBulk(buf []byte, n int, out []bulkEntry) ([]bulkEntry, error) {
	off := 0
	for range n {
		if len(buf)-off < 4 {
			return out, errBulkMalformed
		}
		recLen := int(binary.NativeEndian.Uint32(buf[off:]))
		if recLen < minCommonLen || recLen > len(buf)-off {
			return out, errBulkMalformed
		}
		e, err := decodeBulkRecord(buf[off : off+recLen])
		if err != nil {
			return out, err
		}
		out = append(out, e)
		off += recLen
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
	name, err := bulkName(rec)
	if err != nil {
		return bulkEntry{}, err
	}
	e := bulkEntry{name: name}
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
		// off_t is signed; a negative allocation is treated as unknown.
		if v := ne.Uint64(rec[offAllocSize:]); v <= math.MaxInt64 {
			e.alloc, e.hasSize = int64(v), true
		}
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

// bulkName resolves the ATTR_CMN_NAME attrreference_t, whose offset is
// relative to the reference itself and whose length includes the NUL.
func bulkName(rec []byte) (string, error) {
	ne := binary.NativeEndian
	rel := int64(ne.Uint32(rec[offNameRef:])) // attrreference_t offset is an int32
	if rel >= 1<<31 {
		rel -= 1 << 32
	}
	size := int64(ne.Uint32(rec[offNameRef+4:]))
	start := offNameRef + rel
	if start < 0 || size < 1 || start+size > int64(len(rec)) {
		return "", errBulkMalformed
	}
	b := rec[start : start+size]
	if i := indexNUL(b); i >= 0 {
		b = b[:i]
	}
	if len(b) == 0 {
		return "", errBulkMalformed
	}
	return string(b), nil
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

func indexNUL(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}

func openDirFD(path string) (int, error) {
	for {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// getattrlistbulk wraps getattrlistbulk(2), which x/sys/unix does not
// expose. It returns the number of records packed into buf; 0 means the
// directory is exhausted.
func getattrlistbulk(fd int, buf []byte) (int, error) {
	al := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Commonattr:  bulkCommonAttrs,
		Fileattr:    bulkFileAttrs,
	}
	for {
		// x/sys/unix has no libSystem wrapper for getattrlistbulk; Syscall6
		// on darwin still goes through libSystem's syscall(2) entry.
		r, _, errno := unix.Syscall6(unix.SYS_GETATTRLISTBULK, //nolint:staticcheck // SA1019: no libSystem wrapper exists

			uintptr(fd),
			uintptr(unsafe.Pointer(&al)),     // #nosec G103 -- kernel ABI struct
			uintptr(unsafe.Pointer(&buf[0])), // #nosec G103 -- kernel fills buf, bounded by len
			uintptr(len(buf)),
			uintptr(unix.FSOPT_PACK_INVAL_ATTRS),
			0)
		switch errno {
		case 0:
			return int(r), nil //nolint:gosec // record count fits in int
		case unix.EINTR:
			continue
		default:
			return 0, errno
		}
	}
}
