package heap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ObjectNode is one heap object: its class, shallow size, and the object ids it
// references (out-edges). It is the unit of the reference graph a dominator /
// retained-size analysis walks.
type ObjectNode struct {
	ID          uint64
	ClassName   string
	ShallowSize int64
	Out         []uint64
}

// ObjectGraph is the parsed HPROF object reference graph.
type ObjectGraph struct {
	Nodes  map[uint64]*ObjectNode
	Roots  []uint64
	IDSize int
	// Unresolved counts instances whose class layout was never seen, so their
	// reference fields could not be walked (should be 0 for a well-formed dump).
	Unresolved int
}

// classLayout captures a class's instance-field types (declaration order) and
// its superclass, so an INSTANCE_DUMP's field bytes can be walked.
type classLayout struct {
	superID    uint64
	fieldTypes []byte
	instSize   uint32
}

type rawInstance struct {
	objID   uint64
	classID uint64
	fields  []byte
}

type graphState struct {
	idSize      int
	strings     map[uint64]string
	classNameID map[uint64]uint64
	layouts     map[uint64]classLayout
	roots       map[uint64]bool
	nodes       map[uint64]*ObjectNode
	instances   []rawInstance
}

// ParseObjectGraph streams a .hprof dump and returns its object reference graph
// (objects with out-edges and the GC-root set). It is independent of the
// histogram path and does not modify it.
func ParseObjectGraph(r io.Reader) (*ObjectGraph, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	hr := &hprofReader{r: br}
	if err := readHPROFHeader(hr); err != nil {
		return nil, err
	}
	st := &graphState{
		idSize:      hr.idSize,
		strings:     map[uint64]string{},
		classNameID: map[uint64]uint64{},
		layouts:     map[uint64]classLayout{},
		roots:       map[uint64]bool{},
		nodes:       map[uint64]*ObjectNode{},
	}

	for {
		tag, err := hr.u1()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hprof: read record tag: %w", err)
		}
		if _, err := hr.u4(); err != nil { // timestamp delta
			return nil, err
		}
		length, err := hr.u4()
		if err != nil {
			return nil, err
		}
		if err := st.readRecord(hr, tag, int64(length)); err != nil {
			return nil, err
		}
	}

	return st.finalize(), nil
}

func (st *graphState) readRecord(hr *hprofReader, tag byte, length int64) error {
	switch tag {
	case hprofTagString:
		return st.readString(hr, length)
	case hprofTagLoadClass:
		return st.readLoadClass(hr)
	case hprofTagHeapDump, hprofTagHeapDumpSegment:
		return st.readHeapDump(hr, length)
	default:
		return hr.skip(length)
	}
}

func (st *graphState) readString(hr *hprofReader, length int64) error {
	id, err := hr.id()
	if err != nil {
		return err
	}
	n := length - int64(hr.idSize)
	if n < 0 || n > maxHPROFStringBytes {
		return fmt.Errorf("hprof: bad string length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(hr.r, body); err != nil {
		return err
	}
	st.strings[id] = string(body)
	return nil
}

func (st *graphState) readLoadClass(hr *hprofReader) error {
	if _, err := hr.u4(); err != nil {
		return err
	}
	classObjID, err := hr.id()
	if err != nil {
		return err
	}
	if _, err := hr.u4(); err != nil {
		return err
	}
	nameID, err := hr.id()
	if err != nil {
		return err
	}
	st.classNameID[classObjID] = nameID
	return nil
}

func (st *graphState) readHeapDump(hr *hprofReader, length int64) error {
	lr := &io.LimitedReader{R: hr.r, N: length}
	sub := &hprofReader{r: bufio.NewReader(lr), idSize: hr.idSize}
	for {
		tag, err := sub.u1()
		if errors.Is(err, io.EOF) {
			if lr.N != 0 {
				return fmt.Errorf("hprof: truncated heap dump: %w", io.ErrUnexpectedEOF)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if err := st.readHeapSub(sub, tag); err != nil {
			return err
		}
	}
}

func (st *graphState) readHeapSub(sub *hprofReader, tag byte) error {
	if n, ok := rootObjectIDBytes(sub.idSize, tag); ok {
		return st.readRoot(sub, n)
	}
	switch tag {
	case hprofClassDump:
		return st.readClassDump(sub)
	case hprofInstanceDump:
		return st.readInstanceDump(sub)
	case hprofObjectArrayDump:
		return st.readObjectArray(sub)
	case hprofPrimArrayDump:
		return st.readPrimArray(sub)
	case hprofHeapDumpInfo:
		return sub.skip(4 + int64(sub.idSize))
	default:
		return fmt.Errorf("hprof: unknown heap-dump sub-record tag 0x%02x", tag)
	}
}

// rootObjectIDBytes reports, for a ROOT_* sub-record, the total body size and
// that its first id field is the rooted object. Non-root sub-records return ok
// false.
func rootObjectIDBytes(idSize int, tag byte) (int64, bool) {
	id := int64(idSize)
	switch tag {
	case hprofRootUnknown, hprofRootStickyClass, hprofRootMonitorUsed,
		hprofRootInterned, hprofRootFinalizing, hprofRootDebugger,
		hprofRootRefCleanup, hprofRootVMInternal:
		return id, true
	case hprofRootJNIGlobal:
		return id + id, true
	case hprofRootNativeStack, hprofRootThreadBlock:
		return id + 4, true
	case hprofRootJNILocal, hprofRootJavaFrame, hprofRootThreadObject, hprofRootJNIMonitor:
		return id + 8, true
	default:
		return 0, false
	}
}

func (st *graphState) readRoot(sub *hprofReader, totalBytes int64) error {
	objID, err := sub.id()
	if err != nil {
		return err
	}
	if objID != 0 {
		st.roots[objID] = true
	}
	return sub.skip(totalBytes - int64(sub.idSize))
}

func (st *graphState) readClassDump(sub *hprofReader) error {
	classObjID, err := sub.id()
	if err != nil {
		return err
	}
	if _, err := sub.u4(); err != nil { // stack trace serial
		return err
	}
	superID, err := sub.id()
	if err != nil {
		return err
	}
	for i := 0; i < 5; i++ { // loader, signers, protection domain, reserved1, reserved2
		if _, err := sub.id(); err != nil {
			return err
		}
	}
	instSize, err := sub.u4()
	if err != nil {
		return err
	}
	if err := skipClassPool(sub); err != nil {
		return err
	}
	if err := skipStaticFields(sub); err != nil {
		return err
	}
	types, err := readInstanceFieldTypes(sub)
	if err != nil {
		return err
	}
	st.layouts[classObjID] = classLayout{superID: superID, fieldTypes: types, instSize: instSize}
	return nil
}

// readInstanceFieldTypes reads the instance-field section, returning the field
// types in declaration order.
func readInstanceFieldTypes(sub *hprofReader) ([]byte, error) {
	count, err := sub.u2()
	if err != nil {
		return nil, err
	}
	types := make([]byte, 0, count)
	for i := 0; i < int(count); i++ {
		if _, err := sub.id(); err != nil { // field name string id
			return nil, err
		}
		typ, err := sub.u1()
		if err != nil {
			return nil, err
		}
		types = append(types, typ)
	}
	return types, nil
}

func (st *graphState) readInstanceDump(sub *hprofReader) error {
	objID, err := sub.id()
	if err != nil {
		return err
	}
	if _, err := sub.u4(); err != nil {
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
	fields := make([]byte, nbytes)
	if _, err := io.ReadFull(sub.r, fields); err != nil {
		return err
	}
	st.instances = append(st.instances, rawInstance{objID: objID, classID: classID, fields: fields})
	st.nodes[objID] = &ObjectNode{ID: objID} // class + size + edges filled in finalize
	return nil
}

func (st *graphState) readObjectArray(sub *hprofReader) error {
	objID, err := sub.id()
	if err != nil {
		return err
	}
	if _, err := sub.u4(); err != nil {
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
	out := make([]uint64, 0, numElems)
	for i := 0; i < int(numElems); i++ {
		ref, err := sub.id()
		if err != nil {
			return err
		}
		if ref != 0 {
			out = append(out, ref)
		}
	}
	st.nodes[objID] = &ObjectNode{
		ID:          objID,
		ClassName:   st.className(arrayClassID),
		ShallowSize: hprofArrayHeaderBytes + int64(numElems)*int64(sub.idSize),
		Out:         out,
	}
	return nil
}

func (st *graphState) readPrimArray(sub *hprofReader) error {
	objID, err := sub.id()
	if err != nil {
		return err
	}
	if _, err := sub.u4(); err != nil {
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
	st.nodes[objID] = &ObjectNode{
		ID:          objID,
		ClassName:   primArrayName(elemType),
		ShallowSize: hprofArrayHeaderBytes + int64(numElems)*elemSize,
	}
	return sub.skip(int64(numElems) * elemSize)
}

func (st *graphState) className(classObjID uint64) string {
	nameID, ok := st.classNameID[classObjID]
	if !ok {
		return fmt.Sprintf("unknown-0x%x", classObjID)
	}
	if s, ok := st.strings[nameID]; ok {
		return s
	}
	return fmt.Sprintf("unknown-0x%x", classObjID)
}

// finalize resolves each instance's class name, shallow size, and reference
// out-edges now that every class layout is known.
func (st *graphState) finalize() *ObjectGraph {
	for _, inst := range st.instances {
		node := st.nodes[inst.objID]
		if node == nil {
			continue
		}
		layout, ok := st.layouts[inst.classID]
		if !ok {
			st.nodesUnresolved(node, inst)
			continue
		}
		node.ClassName = st.className(inst.classID)
		node.ShallowSize = int64(layout.instSize)
		node.Out = st.instanceRefs(inst)
	}

	g := &ObjectGraph{Nodes: st.nodes, IDSize: st.idSize}
	for id := range st.roots {
		if _, ok := st.nodes[id]; ok {
			g.Roots = append(g.Roots, id)
		}
	}
	for _, inst := range st.instances {
		if _, ok := st.layouts[inst.classID]; !ok {
			g.Unresolved++
		}
	}
	return g
}

func (st *graphState) nodesUnresolved(node *ObjectNode, inst rawInstance) {
	node.ClassName = st.className(inst.classID)
	node.ShallowSize = int64(len(inst.fields))
}

// instanceRefs walks an instance's field bytes across its superclass chain,
// returning the object ids in its reference fields.
func (st *graphState) instanceRefs(inst rawInstance) []uint64 {
	var out []uint64
	offset := 0
	classID := inst.classID
	for classID != 0 {
		layout, ok := st.layouts[classID]
		if !ok {
			break
		}
		for _, typ := range layout.fieldTypes {
			size := int(hprofTypeSize(typ, st.idSize))
			if offset+size > len(inst.fields) {
				return out // truncated / mismatched; stop safely
			}
			if typ == hprofTypeObject {
				ref := readID(inst.fields[offset:offset+size], st.idSize)
				if ref != 0 {
					out = append(out, ref)
				}
			}
			offset += size
		}
		classID = layout.superID
	}
	return out
}

func readID(b []byte, idSize int) uint64 {
	if idSize == 4 {
		return uint64(binary.BigEndian.Uint32(b[:4]))
	}
	return binary.BigEndian.Uint64(b[:8])
}
