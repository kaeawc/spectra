//go:build darwin

package storagestate

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The log walker's getattrlistbulk attribute set: it needs modification
// times but no inode identity (it does not dedupe hard links).
const (
	logCommonAttrs = unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_NAME |
		unix.ATTR_CMN_OBJTYPE | unix.ATTR_CMN_MODTIME | unix.ATTR_CMN_ERROR
	logFileAttrs = unix.ATTR_FILE_ALLOCSIZE

	// Record layout under FSOPT_PACK_INVAL_ATTRS, as for bulkCommonAttrs.
	logOffNameRef    = offError + 4       // attrreference_t
	logOffObjType    = logOffNameRef + 8  // fsobj_type_t
	logOffModTime    = logOffObjType + 4  // struct timespec {int64 sec, int64 nsec}
	logOffAllocSize  = logOffModTime + 16 // off_t (file attrs, non-directories)
	logMinCommonLen  = logOffAllocSize
	logMinFileRecLen = logOffAllocSize + 8
)

// logEntry is one decoded log-walk record.
type logEntry struct {
	name    []byte // aliases the result buffer
	isDir   bool
	errno   uint32
	mtime   time.Time
	alloc   int64
	hasType bool
	hasMod  bool
	hasSize bool
}

// readLogDirBulk lists path with getattrlistbulk(2), returning its
// subdirectories and the log-shaped files directly in it. It reports false,
// with nothing returned, when the bulk reader cannot handle the directory,
// so the caller can fall back to ReadDir. A directory that cannot be opened
// yields nothing, as under ReadDir.
func readLogDirBulk(path, owner string) ([]string, []LogFile, bool) {
	fd, err := openDirFD(path)
	if err != nil {
		return nil, nil, true
	}
	defer unix.Close(fd) //nolint:errcheck // read-only directory descriptor

	bp := bulkBufPool.Get().(*[]byte)
	defer bulkBufPool.Put(bp)
	buf := *bp

	r := logDirReader{
		path:  path,
		owner: owner,
		// A child path contains "/Logs/" exactly when path+"/" does, since
		// a name never holds a separator.
		inLogs: strings.Contains(path, "/Logs/") || strings.HasSuffix(path, "/Logs"),
	}
	for first := true; ; first = false {
		n, err := getattrlistbulkAttrs(fd, buf, logCommonAttrs, logFileAttrs)
		if err != nil || n == 0 {
			// Like ReadDir, keep what was read before a mid-directory error.
			return r.dirs, r.found, err == nil || !first
		}
		nd, nf := len(r.dirs), len(r.found)
		if err := r.batch(buf, n); err != nil {
			if first {
				return nil, nil, false
			}
			return r.dirs[:nd], r.found[:nf], true
		}
	}
}

// logDirReader accumulates one directory's subdirectories and log-shaped
// files across getattrlistbulk batches.
type logDirReader struct {
	path   string
	owner  string
	inLogs bool
	dirs   []string
	found  []LogFile
}

// batch decodes and applies n records from buf.
func (r *logDirReader) batch(buf []byte, n int) error {
	off := 0
	for range n {
		rec, err := nextBulkRecord(buf, off, logMinCommonLen)
		if err != nil {
			return err
		}
		off += len(rec)
		e, err := decodeLogRecord(rec)
		if err != nil {
			return err
		}
		r.entry(&e)
	}
	return nil
}

func (r *logDirReader) entry(e *logEntry) {
	switch {
	case e.hasType && e.isDir:
		r.dirs = append(r.dirs, joinName(r.path, e.name))
	case e.hasType && e.errno == 0 && !r.inLogs && !bytes.HasSuffix(e.name, []byte(".log")):
		// A file that is not log-shaped; skip without building its path.
	case e.errno != 0 || !e.hasType || !e.hasSize || !e.hasMod:
		// The filesystem could not supply what this entry needs; lstat it
		// so semantics stay those of ReadDir + Info.
		r.lstat(joinName(r.path, e.name))
	default:
		r.found = append(r.found, LogFile{
			Path:      joinName(r.path, e.name),
			Owner:     r.owner,
			SizeBytes: allocBlocks(e.alloc),
			ModTime:   e.mtime,
		})
	}
}

func (r *logDirReader) lstat(full string) {
	fi, err := os.Lstat(full)
	switch {
	case err != nil:
	case fi.IsDir():
		r.dirs = append(r.dirs, full)
	case isLogShapedFile(full):
		r.found = append(r.found, logFileFor(full, r.owner, fi))
	}
}

func decodeLogRecord(rec []byte) (logEntry, error) {
	ne := binary.NativeEndian
	retCommon := ne.Uint32(rec[offReturned:])
	retFile := ne.Uint32(rec[offReturned+12:])
	if retCommon&unix.ATTR_CMN_NAME == 0 {
		return logEntry{}, errBulkMalformed
	}
	name, err := bulkNameAt(rec, logOffNameRef)
	if err != nil {
		return logEntry{}, err
	}
	e := logEntry{name: name}
	if retCommon&unix.ATTR_CMN_ERROR != 0 {
		e.errno = ne.Uint32(rec[offError:])
	}
	if retCommon&unix.ATTR_CMN_OBJTYPE != 0 {
		e.hasType, e.isDir = true, ne.Uint32(rec[logOffObjType:]) == vDIR
	}
	if e.isDir {
		return e, nil
	}
	if retCommon&unix.ATTR_CMN_MODTIME != 0 {
		// time_t and long are signed; reinterpret the packed bits.
		sec := int64(ne.Uint64(rec[logOffModTime:]))    // #nosec G115 -- signed reinterpretation
		nsec := int64(ne.Uint64(rec[logOffModTime+8:])) // #nosec G115 -- signed reinterpretation
		e.mtime, e.hasMod = time.Unix(sec, nsec), true
	}
	if len(rec) >= logMinFileRecLen && retFile&unix.ATTR_FILE_ALLOCSIZE != 0 {
		e.alloc, e.hasSize = allocAt(rec, logOffAllocSize)
	}
	return e, nil
}
