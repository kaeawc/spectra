package heap

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// HPROF binary format support: parse a JVM .hprof heap dump into a class
// histogram (instance counts + shallow bytes per class). This is the reusable
// building block behind dump-vs-live and dump-vs-dump comparison via the
// existing DefaultAnalyzer — the object reference graph (dominator tree,
// retained sizes, paths-to-GC-roots) is a later stage.
//
// Reference: the HPROF binary format as emitted by HotSpot
// (jcmd GC.heap_dump / jmap -dump), versions JAVA PROFILE 1.0.1/1.0.2/1.0.3.

// Top-level record tags.
const (
	hprofTagString          = 0x01
	hprofTagLoadClass       = 0x02
	hprofTagHeapDump        = 0x0C
	hprofTagHeapDumpSegment = 0x1C
	hprofTagHeapDumpEnd     = 0x2C
)

// Heap-dump sub-record tags.
const (
	hprofRootJNIGlobal    = 0x01
	hprofRootJNILocal     = 0x02
	hprofRootJavaFrame    = 0x03
	hprofRootNativeStack  = 0x04
	hprofRootStickyClass  = 0x05
	hprofRootThreadBlock  = 0x06
	hprofRootMonitorUsed  = 0x07
	hprofRootThreadObject = 0x08
	hprofClassDump        = 0x20
	hprofInstanceDump     = 0x21
	hprofObjectArrayDump  = 0x22
	hprofPrimArrayDump    = 0x23
	hprofRootInterned     = 0x89
	hprofRootFinalizing   = 0x8A
	hprofRootDebugger     = 0x8B
	hprofRootRefCleanup   = 0x8C
	hprofRootVMInternal   = 0x8D
	hprofRootJNIMonitor   = 0x8E
	hprofHeapDumpInfo     = 0xFE
	hprofRootUnknown      = 0xFF
)

// Basic-type tags used by CLASS_DUMP fields and PRIMITIVE_ARRAY_DUMP.
const (
	hprofTypeObject  = 2
	hprofTypeBoolean = 4
	hprofTypeChar    = 5
	hprofTypeFloat   = 6
	hprofTypeDouble  = 7
	hprofTypeByte    = 8
	hprofTypeShort   = 9
	hprofTypeInt     = 10
	hprofTypeLong    = 11
)

// hprofArrayHeaderBytes is the fixed per-array object-header estimate added to
// array shallow sizes. Unlike instance sizes (authoritative, from CLASS_DUMP),
// array headers are not in the dump; this constant is close to HotSpot's array
// header with compressed oops. Diffs — the primary use — are robust to it.
const hprofArrayHeaderBytes = 16

// maxHPROFStringBytes bounds a single STRING_IN_UTF8 record body. HPROF string
// records hold symbol names (classes, fields, threads), which are small; the
// cap guards against a corrupt or hostile length field driving a multi-GiB
// allocation before the body is even read (CWE-400).
const maxHPROFStringBytes = 64 << 20 // 64 MiB

// HPROFParser implements heap.Parser for JVM .hprof dumps.
type HPROFParser struct{}

// Runtime returns the parser's supported runtime.
func (HPROFParser) Runtime() Runtime { return RuntimeJVM }

// ParseSnapshot parses an in-memory .hprof dump. For large dumps prefer
// ParseHPROF (streaming) or ParseHPROFFile.
func (HPROFParser) ParseSnapshot(out []byte) (Snapshot, error) {
	return ParseHPROF(bytes.NewReader(out))
}

// ParseHPROFFile streams and parses the .hprof file at path.
func ParseHPROFFile(path string) (Histogram, error) {
	f, err := os.Open(path)
	if err != nil {
		return Histogram{}, err
	}
	defer f.Close()
	return ParseHPROF(f)
}

type hprofReader struct {
	r      *bufio.Reader
	idSize int
	buf    [8]byte
}

func (h *hprofReader) u1() (byte, error) { return h.r.ReadByte() }
func (h *hprofReader) u2() (uint16, error) {
	if _, err := io.ReadFull(h.r, h.buf[:2]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(h.buf[:2]), nil
}
func (h *hprofReader) u4() (uint32, error) {
	if _, err := io.ReadFull(h.r, h.buf[:4]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(h.buf[:4]), nil
}
func (h *hprofReader) u8() (uint64, error) {
	if _, err := io.ReadFull(h.r, h.buf[:8]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(h.buf[:8]), nil
}
func (h *hprofReader) id() (uint64, error) {
	if _, err := io.ReadFull(h.r, h.buf[:h.idSize]); err != nil {
		return 0, err
	}
	if h.idSize == 4 {
		return uint64(binary.BigEndian.Uint32(h.buf[:4])), nil
	}
	return binary.BigEndian.Uint64(h.buf[:8]), nil
}
func (h *hprofReader) skip(n int64) error {
	_, err := io.CopyN(io.Discard, h.r, n)
	return err
}

// hprofState accumulates the data needed to build a histogram.
type hprofState struct {
	strings       map[uint64]string
	classNameID   map[uint64]uint64 // class object id -> class name string id
	classInstSize map[uint64]uint32 // class object id -> per-instance shallow size
	instCount     map[uint64]int64  // class object id -> instance count
	arrayCount    map[uint64]int64  // array class id -> array count (object arrays)
	arrayBytes    map[uint64]int64  // array class id -> total shallow bytes
	primCount     map[byte]int64    // element type -> primitive-array count
	primBytes     map[byte]int64    // element type -> total shallow bytes
}

func newHPROFState() *hprofState {
	return &hprofState{
		strings:       map[uint64]string{},
		classNameID:   map[uint64]uint64{},
		classInstSize: map[uint64]uint32{},
		instCount:     map[uint64]int64{},
		arrayCount:    map[uint64]int64{},
		arrayBytes:    map[uint64]int64{},
		primCount:     map[byte]int64{},
		primBytes:     map[byte]int64{},
	}
}

// ParseHPROF streams a .hprof dump and returns a class Histogram.
func ParseHPROF(r io.Reader) (Histogram, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	hr := &hprofReader{r: br}

	if err := readHPROFHeader(hr); err != nil {
		return Histogram{}, err
	}

	st := newHPROFState()
	for {
		tag, err := hr.u1()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Histogram{}, fmt.Errorf("hprof: read record tag: %w", err)
		}
		if _, err := hr.u4(); err != nil { // record timestamp (relative micros); unused
			return Histogram{}, fmt.Errorf("hprof: read record time: %w", err)
		}
		length, err := hr.u4()
		if err != nil {
			return Histogram{}, fmt.Errorf("hprof: read record length: %w", err)
		}
		if err := readHPROFRecord(hr, st, tag, int64(length)); err != nil {
			return Histogram{}, err
		}
	}
	return buildHistogram(st), nil
}

func readHPROFHeader(hr *hprofReader) error {
	// Null-terminated format-name string, e.g. "JAVA PROFILE 1.0.2".
	name, err := hr.r.ReadString(0x00)
	if err != nil {
		return fmt.Errorf("hprof: read header name: %w", err)
	}
	if !strings.HasPrefix(name, "JAVA PROFILE") {
		return fmt.Errorf("hprof: not a JAVA PROFILE dump (got %q)", strings.TrimRight(name, "\x00"))
	}
	idSize, err := hr.u4()
	if err != nil {
		return fmt.Errorf("hprof: read id size: %w", err)
	}
	if idSize != 4 && idSize != 8 {
		return fmt.Errorf("hprof: unsupported identifier size %d", idSize)
	}
	hr.idSize = int(idSize)
	if _, err := hr.u8(); err != nil { // timestamp
		return fmt.Errorf("hprof: read timestamp: %w", err)
	}
	return nil
}

func readHPROFRecord(hr *hprofReader, st *hprofState, tag byte, length int64) error {
	switch tag {
	case hprofTagString:
		return readStringRecord(hr, st, length)
	case hprofTagLoadClass:
		return readLoadClassRecord(hr, st)
	case hprofTagHeapDump, hprofTagHeapDumpSegment:
		return readHeapDump(hr, st, length)
	default:
		// hprofTagHeapDumpEnd has length 0; everything else we skip wholesale.
		return hr.skip(length)
	}
}

func readStringRecord(hr *hprofReader, st *hprofState, length int64) error {
	id, err := hr.id()
	if err != nil {
		return fmt.Errorf("hprof: string id: %w", err)
	}
	n := length - int64(hr.idSize)
	if n < 0 {
		return fmt.Errorf("hprof: negative string length %d", n)
	}
	if n > maxHPROFStringBytes {
		return fmt.Errorf("hprof: string record too large (%d bytes, max %d)", n, maxHPROFStringBytes)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(hr.r, body); err != nil {
		return fmt.Errorf("hprof: string body: %w", err)
	}
	st.strings[id] = string(body)
	return nil
}

func readLoadClassRecord(hr *hprofReader, st *hprofState) error {
	if _, err := hr.u4(); err != nil { // class serial number
		return err
	}
	classObjID, err := hr.id()
	if err != nil {
		return err
	}
	if _, err := hr.u4(); err != nil { // stack trace serial
		return err
	}
	nameID, err := hr.id()
	if err != nil {
		return err
	}
	st.classNameID[classObjID] = nameID
	return nil
}

// readHeapDump parses the sub-records inside a HEAP DUMP / HEAP DUMP SEGMENT,
// consuming exactly length bytes via a limited reader. If the underlying stream
// ends before the declared length is consumed (a truncated dump), it returns
// io.ErrUnexpectedEOF rather than a silently-partial histogram.
func readHeapDump(hr *hprofReader, st *hprofState, length int64) error {
	lr := &io.LimitedReader{R: hr.r, N: length}
	sub := &hprofReader{r: bufio.NewReader(lr), idSize: hr.idSize}
	for {
		tag, err := sub.u1()
		if errors.Is(err, io.EOF) {
			if lr.N != 0 {
				return fmt.Errorf("hprof: truncated heap dump (%d of %d bytes missing): %w", lr.N, length, io.ErrUnexpectedEOF)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("hprof: heap-dump sub-tag: %w", err)
		}
		if err := readHeapSubRecord(sub, st, tag); err != nil {
			return err
		}
	}
}

func readHeapSubRecord(sub *hprofReader, st *hprofState, tag byte) error {
	if fixed, ok := hprofRootSkipBytes(sub.idSize, tag); ok {
		return sub.skip(fixed)
	}
	switch tag {
	case hprofClassDump:
		return readClassDump(sub, st)
	case hprofInstanceDump:
		return readInstanceDump(sub, st)
	case hprofObjectArrayDump:
		return readObjectArrayDump(sub, st)
	case hprofPrimArrayDump:
		return readPrimArrayDump(sub, st)
	default:
		return fmt.Errorf("hprof: unknown heap-dump sub-record tag 0x%02x", tag)
	}
}

// hprofRootSkipBytes returns the fixed body size (after the sub-tag) of the
// root / info sub-records that carry no histogram data, so they can be skipped
// without desyncing the stream.
func hprofRootSkipBytes(idSize int, tag byte) (int64, bool) {
	id := int64(idSize)
	switch tag {
	case hprofRootUnknown, hprofRootStickyClass, hprofRootMonitorUsed,
		hprofRootInterned, hprofRootFinalizing, hprofRootDebugger,
		hprofRootRefCleanup, hprofRootVMInternal:
		return id, true
	case hprofRootJNIGlobal:
		return id + id, true // object id + JNI global ref id
	case hprofRootNativeStack, hprofRootThreadBlock:
		return id + 4, true // object id + u4
	case hprofRootJNILocal, hprofRootJavaFrame, hprofRootThreadObject, hprofRootJNIMonitor:
		return id + 8, true // object id + two u4
	case hprofHeapDumpInfo:
		return 4 + id, true // u4 heap id + heap name string id
	default:
		return 0, false
	}
}

func readClassDump(sub *hprofReader, st *hprofState) error {
	classObjID, err := sub.id()
	if err != nil {
		return err
	}
	// stack trace serial, then 6 ids (super, loader, signers, protection
	// domain, reserved1, reserved2).
	if _, err := sub.u4(); err != nil {
		return err
	}
	for i := 0; i < 6; i++ {
		if _, err := sub.id(); err != nil {
			return err
		}
	}
	instSize, err := sub.u4()
	if err != nil {
		return err
	}
	st.classInstSize[classObjID] = instSize

	// Constant pool: u2 count, each {u2 index, u1 type, value}.
	if err := skipClassPool(sub); err != nil {
		return err
	}
	// Static fields: u2 count, each {id name, u1 type, value}.
	if err := skipStaticFields(sub); err != nil {
		return err
	}
	// Instance fields: u2 count, each {id name, u1 type} (no value).
	return skipInstanceFields(sub)
}

func skipClassPool(sub *hprofReader) error {
	count, err := sub.u2()
	if err != nil {
		return err
	}
	for i := 0; i < int(count); i++ {
		if _, err := sub.u2(); err != nil { // constant pool index
			return err
		}
		typ, err := sub.u1()
		if err != nil {
			return err
		}
		if err := sub.skip(hprofTypeSize(typ, sub.idSize)); err != nil {
			return err
		}
	}
	return nil
}

func skipStaticFields(sub *hprofReader) error {
	count, err := sub.u2()
	if err != nil {
		return err
	}
	for i := 0; i < int(count); i++ {
		if _, err := sub.id(); err != nil { // field name string id
			return err
		}
		typ, err := sub.u1()
		if err != nil {
			return err
		}
		if err := sub.skip(hprofTypeSize(typ, sub.idSize)); err != nil {
			return err
		}
	}
	return nil
}

func skipInstanceFields(sub *hprofReader) error {
	count, err := sub.u2()
	if err != nil {
		return err
	}
	for i := 0; i < int(count); i++ {
		if _, err := sub.id(); err != nil { // field name string id
			return err
		}
		if _, err := sub.u1(); err != nil { // type
			return err
		}
	}
	return nil
}

func readInstanceDump(sub *hprofReader, st *hprofState) error {
	if _, err := sub.id(); err != nil { // object id
		return err
	}
	if _, err := sub.u4(); err != nil { // stack trace serial
		return err
	}
	classID, err := sub.id()
	if err != nil {
		return err
	}
	nbytes, err := sub.u4()
	if err != nil {
		return err
	}
	st.instCount[classID]++
	return sub.skip(int64(nbytes))
}

func readObjectArrayDump(sub *hprofReader, st *hprofState) error {
	if _, err := sub.id(); err != nil { // array object id
		return err
	}
	if _, err := sub.u4(); err != nil { // stack trace serial
		return err
	}
	numElems, err := sub.u4()
	if err != nil {
		return err
	}
	arrayClassID, err := sub.id()
	if err != nil {
		return err
	}
	st.arrayCount[arrayClassID]++
	st.arrayBytes[arrayClassID] += hprofArrayHeaderBytes + int64(numElems)*int64(sub.idSize)
	return sub.skip(int64(numElems) * int64(sub.idSize))
}

func readPrimArrayDump(sub *hprofReader, st *hprofState) error {
	if _, err := sub.id(); err != nil { // array object id
		return err
	}
	if _, err := sub.u4(); err != nil { // stack trace serial
		return err
	}
	numElems, err := sub.u4()
	if err != nil {
		return err
	}
	elemType, err := sub.u1()
	if err != nil {
		return err
	}
	elemSize := hprofTypeSize(elemType, sub.idSize)
	st.primCount[elemType]++
	st.primBytes[elemType] += hprofArrayHeaderBytes + int64(numElems)*elemSize
	return sub.skip(int64(numElems) * elemSize)
}

func hprofTypeSize(typ byte, idSize int) int64 {
	switch typ {
	case hprofTypeObject:
		return int64(idSize)
	case hprofTypeBoolean, hprofTypeByte:
		return 1
	case hprofTypeChar, hprofTypeShort:
		return 2
	case hprofTypeFloat, hprofTypeInt:
		return 4
	case hprofTypeDouble, hprofTypeLong:
		return 8
	default:
		return 0
	}
}

func (st *hprofState) className(classObjID uint64) string {
	nameID, ok := st.classNameID[classObjID]
	if !ok {
		return fmt.Sprintf("unknown-0x%x", classObjID)
	}
	name := st.strings[nameID]
	if name == "" {
		return fmt.Sprintf("unknown-0x%x", classObjID)
	}
	return strings.ReplaceAll(name, "/", ".")
}

func primArrayName(elemType byte) string {
	switch elemType {
	case hprofTypeBoolean:
		return "[Z"
	case hprofTypeChar:
		return "[C"
	case hprofTypeFloat:
		return "[F"
	case hprofTypeDouble:
		return "[D"
	case hprofTypeByte:
		return "[B"
	case hprofTypeShort:
		return "[S"
	case hprofTypeInt:
		return "[I"
	case hprofTypeLong:
		return "[J"
	default:
		return "[?"
	}
}

func buildHistogram(st *hprofState) Histogram {
	var entries []ClassEntry
	for classID, count := range st.instCount {
		size := int64(st.classInstSize[classID])
		entries = append(entries, ClassEntry{
			Instances: count,
			Bytes:     count * size,
			ClassName: st.className(classID),
		})
	}
	for arrayClassID, count := range st.arrayCount {
		entries = append(entries, ClassEntry{
			Instances: count,
			Bytes:     st.arrayBytes[arrayClassID],
			ClassName: st.className(arrayClassID),
		})
	}
	for elemType, count := range st.primCount {
		entries = append(entries, ClassEntry{
			Instances: count,
			Bytes:     st.primBytes[elemType],
			ClassName: primArrayName(elemType),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Bytes != entries[j].Bytes {
			return entries[i].Bytes > entries[j].Bytes
		}
		return entries[i].ClassName < entries[j].ClassName
	})

	var total ClassEntry
	total.ClassName = "Total"
	for i := range entries {
		entries[i].Rank = i + 1
		total.Instances += entries[i].Instances
		total.Bytes += entries[i].Bytes
	}
	return Histogram{Entries: entries, Total: total}
}
