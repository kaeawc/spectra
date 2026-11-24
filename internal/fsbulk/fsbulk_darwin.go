//go:build darwin

package fsbulk

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// BufSize is the getattrlistbulk result buffer per call. Larger buffers mean
// fewer system calls on wide directories.
const BufSize = 128 << 10

var bufPool = sync.Pool{New: func() any {
	b := make([]byte, BufSize)
	return &b
}}

// GetBuf takes a BufSize result buffer from the shared pool.
func GetBuf() *[]byte { return bufPool.Get().(*[]byte) }

// PutBuf returns a buffer taken with GetBuf.
func PutBuf(b *[]byte) { bufPool.Put(b) }

// fsobj_type_t values from <sys/vnode.h>.
const (
	VREG  = 1
	VDIR  = 2
	VBLK  = 3
	VCHR  = 4
	VLNK  = 5
	VSOCK = 6
	VFIFO = 7
)

// Record layout shared by every attribute set that requests
// ATTR_CMN_RETURNED_ATTRS and ATTR_CMN_ERROR under FSOPT_PACK_INVAL_ATTRS:
// every requested attribute is packed, invalid ones zeroed, so offsets do not
// depend on which attributes the filesystem returned. ATTR_CMN_ERROR is
// packed directly after ATTR_CMN_RETURNED_ATTRS; the rest follow in bit
// order starting at OffFirst.
const (
	OffReturned = 4                // attribute_set_t: common, vol, dir, file, fork
	OffError    = OffReturned + 20 // u_int32_t
	OffFirst    = OffError + 4     // first attribute after ATTR_CMN_ERROR
)

// ErrMalformed reports a record that does not fit its buffer or lacks a
// required attribute.
var ErrMalformed = errors.New("getattrlistbulk: malformed record")

// OpenDir opens path as a read-only directory descriptor, retrying EINTR.
func OpenDir(path string) (int, error) {
	for {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// Read wraps getattrlistbulk(2), which x/sys/unix does not expose,
// requesting the given common and file attribute sets with
// FSOPT_PACK_INVAL_ATTRS. It returns the number of records packed into buf;
// 0 means the directory is exhausted.
func Read(fd int, buf []byte, common, file uint32) (int, error) {
	al := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Commonattr:  common,
		Fileattr:    file,
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

// NextRecord returns the record at off, checking its declared length
// against minLen and the buffer.
func NextRecord(buf []byte, off, minLen int) ([]byte, error) {
	if len(buf)-off < 4 {
		return nil, ErrMalformed
	}
	recLen := int(binary.NativeEndian.Uint32(buf[off:]))
	if recLen < minLen || recLen > len(buf)-off {
		return nil, ErrMalformed
	}
	return buf[off : off+recLen], nil
}

// Returned reads a record's returned common and file attribute bits.
func Returned(rec []byte) (common, file uint32) {
	ne := binary.NativeEndian
	return ne.Uint32(rec[OffReturned:]), ne.Uint32(rec[OffReturned+12:])
}

// NameAt resolves the ATTR_CMN_NAME attrreference_t at ref, whose offset is
// relative to the reference itself and whose length includes the NUL. The
// name bytes are returned in place.
func NameAt(rec []byte, ref int) ([]byte, error) {
	ne := binary.NativeEndian
	rel := int64(ne.Uint32(rec[ref:])) // attrreference_t offset is an int32
	if rel >= 1<<31 {
		rel -= 1 << 32
	}
	size := int64(ne.Uint32(rec[ref+4:]))
	start := int64(ref) + rel
	if start < 0 || size < 1 || start+size > int64(len(rec)) {
		return nil, ErrMalformed
	}
	b := rec[start : start+size]
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	if len(b) == 0 {
		return nil, ErrMalformed
	}
	return b, nil
}

// OffT reads an off_t; off_t is signed, and a negative value is treated as
// unknown.
func OffT(rec []byte, off int) (int64, bool) {
	if v := binary.NativeEndian.Uint64(rec[off:]); v <= math.MaxInt64 {
		return int64(v), true
	}
	return 0, false
}

// AllocBlocks rounds an allocation up to whole 512-byte blocks, matching how
// the kernel derives st_blocks from the same allocation for lstat.
func AllocBlocks(alloc int64) int64 {
	return (alloc + 511) / 512 * 512
}

// The entry lister's attribute set: name, type and both sizes, nothing for
// hard-link identity.
const (
	entryCommonAttrs = unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_NAME |
		unix.ATTR_CMN_OBJTYPE | unix.ATTR_CMN_ERROR
	entryFileAttrs = unix.ATTR_FILE_ALLOCSIZE | unix.ATTR_FILE_DATALENGTH

	entryOffNameRef = OffFirst            // attrreference_t
	entryOffObjType = entryOffNameRef + 8 // fsobj_type_t
	entryOffAlloc   = entryOffObjType + 4 // off_t (file attrs, non-directories)
	entryOffDataLen = entryOffAlloc + 8   // off_t
	entryMinLen     = entryOffAlloc
	entryMinFileLen = entryOffDataLen + 8
)

// Entry is one directory entry from Lister.ReadDir.
type Entry struct {
	// Name aliases the lister's buffer: valid only during the callback.
	Name  []byte
	Type  uint32 // fsobj_type_t, valid when HasType
	Errno uint32 // per-entry ATTR_CMN_ERROR, 0 when the attributes are good
	// Alloc is the allocated bytes (ATTR_FILE_ALLOCSIZE, the source of
	// st_blocks); Size is the logical data length (ATTR_FILE_DATALENGTH, the
	// source of st_size). Both are valid when HasSizes.
	Alloc, Size int64
	HasType     bool
	HasSizes    bool
}

// IsDir reports whether the entry is known to be a directory.
func (e *Entry) IsDir() bool { return e.HasType && e.Type == VDIR }

// Complete reports whether the entry carries everything an lstat would:
// a type, and for non-directories both sizes.
func (e *Entry) Complete() bool {
	return e.Errno == 0 && e.HasType && (e.Type == VDIR || e.HasSizes)
}

// Lister is one goroutine's reusable entry-listing state.
type Lister struct {
	bp      *[]byte
	entries []Entry
}

// Release returns the lister's buffer to the pool; the lister stays usable.
func (l *Lister) Release() {
	if l.bp != nil {
		PutBuf(l.bp)
		l.bp = nil
	}
	clear(l.entries)
	l.entries = l.entries[:0]
}

// ReadDir lists path, calling fn for each entry. A batch is decoded in full
// before any of it is delivered, so ReadDir reports false, having delivered
// nothing, when getattrlistbulk cannot handle the directory at all and the
// caller should fall back to ReadDir + lstat. A directory that cannot be
// opened delivers nothing and reports true, as os.Open failing would; an
// error after the first batch keeps what was delivered, as ReadDir does.
func (l *Lister) ReadDir(path string, fn func(*Entry)) bool {
	ok, _ := l.ReadDirErr(path, fn)
	return ok
}

// ReadDirErr is ReadDir that also returns the error that ended a listing it
// handled: the open failure, or the read error after the first batch. It is
// nil when the listing completed or when ok is false.
func (l *Lister) ReadDirErr(path string, fn func(*Entry)) (ok bool, err error) {
	fd, err := OpenDir(path)
	if err != nil {
		return true, err
	}
	defer unix.Close(fd) //nolint:errcheck // read-only directory descriptor
	if l.bp == nil {
		l.bp = GetBuf()
	}
	buf := *l.bp
	for first := true; ; first = false {
		n, err := Read(fd, buf, entryCommonAttrs, entryFileAttrs)
		if err == nil && n > 0 {
			l.entries, err = decodeEntries(buf, n, l.entries[:0])
		}
		if err != nil {
			if first {
				return false, nil
			}
			return true, err
		}
		if n == 0 {
			return true, nil
		}
		for i := range l.entries {
			fn(&l.entries[i])
		}
	}
}

func decodeEntries(buf []byte, n int, out []Entry) ([]Entry, error) {
	off := 0
	for range n {
		rec, err := NextRecord(buf, off, entryMinLen)
		if err != nil {
			return out, err
		}
		off += len(rec)
		e, err := decodeEntry(rec)
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
	return out, nil
}

func decodeEntry(rec []byte) (Entry, error) {
	ne := binary.NativeEndian
	retCommon, retFile := Returned(rec)
	if retCommon&unix.ATTR_CMN_NAME == 0 {
		return Entry{}, ErrMalformed
	}
	name, err := NameAt(rec, entryOffNameRef)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Name: name}
	if retCommon&unix.ATTR_CMN_ERROR != 0 {
		e.Errno = ne.Uint32(rec[OffError:])
	}
	if retCommon&unix.ATTR_CMN_OBJTYPE != 0 {
		e.Type, e.HasType = ne.Uint32(rec[entryOffObjType:]), true
	}
	if e.IsDir() || len(rec) < entryMinFileLen {
		return e, nil
	}
	const sizeBits = unix.ATTR_FILE_ALLOCSIZE | unix.ATTR_FILE_DATALENGTH
	if retFile&sizeBits == sizeBits {
		var okA, okS bool
		e.Alloc, okA = OffT(rec, entryOffAlloc)
		e.Size, okS = OffT(rec, entryOffDataLen)
		e.HasSizes = okA && okS
	}
	return e, nil
}
