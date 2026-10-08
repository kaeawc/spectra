package heap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// javaLangClass is the class every class object is an instance of. Class
// objects are graph nodes so static-field references (the usual way an
// application retains memory) are part of the reference graph.
const javaLangClass = "java.lang.Class"

// ObjectGraph is the parsed HPROF object reference graph in a compact,
// int32-indexed form. Objects are numbered 0..Len()-1 in ascending object-id
// order; out-edges are stored CSR-style (outStart/out) so a multi-million
// object heap costs a handful of flat slices rather than a map of pointers.
type ObjectGraph struct {
	IDSize int
	// Unresolved counts instances whose class layout was never seen, so their
	// reference fields could not be walked (should be 0 for a well-formed dump).
	Unresolved int

	ids      []uint64 // object index -> object id (sorted ascending)
	class    []int32  // object index -> class index
	shallow  []int64  // object index -> shallow size in bytes
	outStart []int    // CSR offsets into out; len = Len()+1
	out      []int32  // out-edge targets (object indices)
	roots    []int32  // GC-rooted object indices (deduplicated)
	gcRoots  []GCRoot // every GC-root record, sorted by Object

	predStart []int // lazily built reverse CSR for path queries
	preds     []int32

	classNames []string         // class index -> class name
	classObjOf map[int32]string // object index of a class object -> its class name
}

// Len returns the number of objects in the graph.
func (g *ObjectGraph) Len() int { return len(g.ids) }

// ID returns the HPROF object id of object i.
func (g *ObjectGraph) ID(i int32) uint64 { return g.ids[i] }

// Index returns the object index for an HPROF object id.
func (g *ObjectGraph) Index(id uint64) (int32, bool) {
	j := sort.Search(len(g.ids), func(k int) bool { return g.ids[k] >= id })
	if j < len(g.ids) && g.ids[j] == id {
		return int32(j), true
	}
	return -1, false
}

// ClassName returns the class name of object i (java.lang.Class for class
// objects).
func (g *ObjectGraph) ClassName(i int32) string { return g.classNames[g.class[i]] }

// DisplayName describes object i: its class name, or "class X" for the class
// object of X.
func (g *ObjectGraph) DisplayName(i int32) string {
	if name, ok := g.classObjOf[i]; ok {
		return "class " + name
	}
	return g.ClassName(i)
}

// ShallowSize returns object i's own size in bytes.
func (g *ObjectGraph) ShallowSize(i int32) int64 { return g.shallow[i] }

// Out returns the object indices object i references. The slice aliases the
// graph's storage and must not be modified.
func (g *ObjectGraph) Out(i int32) []int32 { return g.out[g.outStart[i]:g.outStart[i+1]] }

// Roots returns the GC-rooted object indices.
func (g *ObjectGraph) Roots() []int32 { return g.roots }

// NumEdges returns the number of resolved references in the graph.
func (g *ObjectGraph) NumEdges() int { return len(g.out) }

// classKey identifies a class-table entry: a dumped class object, or a
// synthetic primitive-array type keyed by its element type.
type classKey struct {
	classID  uint64
	primType byte
}

// graphBuilder accumulates objects in parse order with their raw (id-valued)
// references, then resolves them into an ObjectGraph. Each object's references
// live in one contiguous run of rawEdges, so per-object overhead is a few
// scalars rather than a slice header.
type graphBuilder struct {
	idSize   int
	ids      []uint64
	class    []int32
	shallow  []int64
	edgeOff  []int
	edgeCnt  []int32
	rawEdges []uint64
	roots    []rawRoot
	// threadBySerial maps a thread serial number to its thread object id, from
	// ROOT_THREAD_OBJECT records, so frame roots can name their thread.
	threadBySerial map[uint32]uint64

	classIdx   map[classKey]int32
	classKeys  []classKey
	classNames []string // preset names; "" means resolve from LOAD_CLASS
}

func newGraphBuilder(idSize int) *graphBuilder {
	return &graphBuilder{idSize: idSize, classIdx: map[classKey]int32{}, threadBySerial: map[uint32]uint64{}}
}

func (b *graphBuilder) classFor(k classKey, presetName string) int32 {
	if i, ok := b.classIdx[k]; ok {
		return i
	}
	i := int32(len(b.classKeys))
	b.classIdx[k] = i
	b.classKeys = append(b.classKeys, k)
	b.classNames = append(b.classNames, presetName)
	return i
}

// addObject appends an object and returns its parse-order index.
func (b *graphBuilder) addObject(id uint64, class int32, shallow int64, refs []uint64) int {
	p := len(b.ids)
	b.ids = append(b.ids, id)
	b.class = append(b.class, class)
	b.shallow = append(b.shallow, shallow)
	b.edgeOff = append(b.edgeOff, len(b.rawEdges))
	b.edgeCnt = append(b.edgeCnt, 0)
	b.appendRefs(p, refs)
	return p
}

// appendRefs sets object p's references. It must be called at most once per
// object after addObject(…, nil) so the run stays contiguous.
func (b *graphBuilder) appendRefs(p int, refs []uint64) {
	if len(refs) == 0 {
		return
	}
	b.edgeOff[p] = len(b.rawEdges)
	for _, r := range refs {
		if r != 0 {
			b.rawEdges = append(b.rawEdges, r)
		}
	}
	b.edgeCnt[p] = int32(len(b.rawEdges) - b.edgeOff[p])
}

// build sorts objects by id, resolves references to object indices (dropping
// references to objects absent from the dump), and returns the graph. names
// resolves class-table entries whose name was not preset.
func (b *graphBuilder) build(names func(classKey) string) *ObjectGraph {
	n := len(b.ids)
	perm := make([]int32, n)
	for i := range perm {
		perm[i] = int32(i)
	}
	sort.Slice(perm, func(x, y int) bool { return b.ids[perm[x]] < b.ids[perm[y]] })

	g := &ObjectGraph{
		IDSize:     b.idSize,
		ids:        make([]uint64, n),
		class:      make([]int32, n),
		shallow:    make([]int64, n),
		outStart:   make([]int, n+1),
		classObjOf: map[int32]string{},
	}
	for i, p := range perm {
		g.ids[i] = b.ids[p]
		g.class[i] = b.class[p]
		g.shallow[i] = b.shallow[p]
	}
	g.classNames = make([]string, len(b.classKeys))
	for i, k := range b.classKeys {
		g.classNames[i] = b.classNames[i]
		if g.classNames[i] == "" {
			g.classNames[i] = names(k)
		}
	}
	g.resolveEdges(b, perm)
	g.resolveRoots(b.roots, b.threadBySerial)
	for i, k := range b.classKeys {
		if k.primType != 0 || k.classID == 0 {
			continue
		}
		if obj, ok := g.Index(k.classID); ok && g.ClassName(obj) == javaLangClass {
			g.classObjOf[obj] = g.classNames[i]
		}
	}
	return g
}

func (g *ObjectGraph) resolveEdges(b *graphBuilder, perm []int32) {
	g.out = make([]int32, 0, len(b.rawEdges))
	for i, p := range perm {
		g.outStart[i] = len(g.out)
		run := b.rawEdges[b.edgeOff[p] : b.edgeOff[p]+int(b.edgeCnt[p])]
		for _, ref := range run {
			if t, ok := g.Index(ref); ok {
				g.out = append(g.out, t)
			}
		}
	}
	g.outStart[len(perm)] = len(g.out)
}

// classLayout captures a class's instance-field types (declaration order) and
// its superclass, so an INSTANCE_DUMP's field bytes can be walked.
type classLayout struct {
	superID    uint64
	fieldTypes []byte
	instSize   uint32
}

// pendingInstance is an instance whose class layout had not been seen when it
// was read; its field bytes are kept until the end of the stream.
type pendingInstance struct {
	parseIdx int
	classID  uint64
	fields   []byte
}

type graphState struct {
	idSize      int
	strings     map[uint64]string
	classNameID map[uint64]uint64
	layouts     map[uint64]classLayout
	pending     []pendingInstance
	b           *graphBuilder
	classClass  int32
	scratch     []uint64
}

// ParseObjectGraph streams a .hprof dump and returns its object reference graph
// (objects with out-edges and the GC-root set). It is independent of the
// histogram path and does not modify it.
//
// Instances whose class dump has already been seen are reduced to their
// reference ids as they stream past; only instances that precede their class
// dump are buffered whole until the end of the stream.
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
		b:           newGraphBuilder(hr.idSize),
	}
	st.classClass = st.b.classFor(classKey{}, javaLangClass)

	for {
		tag, err := hr.u1()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hprof: read record tag: %w", err)
		}
		if _, err := hr.u4(); err != nil { // timestamp delta
			return nil, fmt.Errorf("hprof: read record time: %w", err)
		}
		length, err := hr.u4()
		if err != nil {
			return nil, fmt.Errorf("hprof: read record length: %w", err)
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
		return fmt.Errorf("hprof: string id: %w", err)
	}
	n := length - int64(hr.idSize)
	if n < 0 || n > maxHPROFStringBytes {
		return fmt.Errorf("hprof: bad string length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(hr.r, body); err != nil {
		return fmt.Errorf("hprof: string body: %w", err)
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
			return fmt.Errorf("hprof: heap-dump sub-tag: %w", err)
		}
		if err := st.readHeapSub(sub, tag); err != nil {
			return err
		}
	}
}

func (st *graphState) readHeapSub(sub *hprofReader, tag byte) error {
	if kind, n, ok := rootRecordLayout(sub.idSize, tag); ok {
		return st.readRoot(sub, kind, n)
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

// readClassDump records the class layout and adds the class object itself as a
// node whose references are its superclass, class loader, and object-typed
// static fields. Class objects have zero shallow size so totals match the
// class histogram.
func (st *graphState) readClassDump(sub *hprofReader) error {
	classObjID, err := sub.id()
	if err != nil {
		return err
	}
	if _, err := sub.u4(); err != nil { // stack trace serial
		return err
	}
	var hdr [6]uint64 // super, loader, signers, protection domain, reserved1, reserved2
	for i := range hdr {
		if hdr[i], err = sub.id(); err != nil {
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
	refs := append(st.scratch[:0], hdr[0], hdr[1])
	refs, err = readStaticRefs(sub, refs)
	if err != nil {
		return err
	}
	types, err := readInstanceFieldTypes(sub)
	if err != nil {
		return err
	}
	st.layouts[classObjID] = classLayout{superID: hdr[0], fieldTypes: types, instSize: instSize}
	st.b.classFor(classKey{classID: classObjID}, "")
	st.b.addObject(classObjID, st.classClass, 0, refs)
	st.scratch = refs
	return nil
}

// readStaticRefs reads the static-field section, appending the non-null object
// references to refs.
func readStaticRefs(sub *hprofReader, refs []uint64) ([]uint64, error) {
	count, err := sub.u2()
	if err != nil {
		return refs, err
	}
	for i := 0; i < int(count); i++ {
		if _, err := sub.id(); err != nil { // field name string id
			return refs, err
		}
		typ, err := sub.u1()
		if err != nil {
			return refs, err
		}
		if typ != hprofTypeObject {
			if err := sub.skip(hprofTypeSize(typ, sub.idSize)); err != nil {
				return refs, err
			}
			continue
		}
		ref, err := sub.id()
		if err != nil {
			return refs, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
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
	class := st.b.classFor(classKey{classID: classID}, "")
	if layout, ok := st.layouts[classID]; ok {
		refs, complete := st.instanceRefs(classID, fields, st.scratch[:0])
		if complete {
			st.b.addObject(objID, class, int64(layout.instSize), refs)
			st.scratch = refs
			return nil
		}
	}
	p := st.b.addObject(objID, class, int64(nbytes), nil)
	st.pending = append(st.pending, pendingInstance{parseIdx: p, classID: classID, fields: fields})
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
	refs := st.scratch[:0]
	for i := uint32(0); i < numElems; i++ {
		ref, err := sub.id()
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	class := st.b.classFor(classKey{classID: arrayClassID}, "")
	shallow := hprofArrayHeaderBytes + int64(numElems)*int64(sub.idSize)
	st.b.addObject(objID, class, shallow, refs)
	st.scratch = refs
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
	class := st.b.classFor(classKey{primType: elemType}, primArrayName(elemType))
	st.b.addObject(objID, class, hprofArrayHeaderBytes+int64(numElems)*elemSize, nil)
	return sub.skip(int64(numElems) * elemSize)
}

func (st *graphState) className(k classKey) string {
	nameID, ok := st.classNameID[k.classID]
	if !ok {
		return fmt.Sprintf("unknown-0x%x", k.classID)
	}
	if s, ok := st.strings[nameID]; ok && s != "" {
		return strings.ReplaceAll(s, "/", ".")
	}
	return fmt.Sprintf("unknown-0x%x", k.classID)
}

// finalize resolves instances that preceded their class dump, then builds the
// compact graph.
func (st *graphState) finalize() *ObjectGraph {
	for _, inst := range st.pending {
		layout, ok := st.layouts[inst.classID]
		if !ok {
			continue
		}
		st.b.shallow[inst.parseIdx] = int64(layout.instSize)
		refs, _ := st.instanceRefs(inst.classID, inst.fields, st.scratch[:0])
		st.b.appendRefs(inst.parseIdx, refs)
		st.scratch = refs
	}
	g := st.b.build(st.className)
	for _, inst := range st.pending {
		if _, ok := st.layouts[inst.classID]; !ok {
			g.Unresolved++
		}
	}
	st.pending = nil
	return g
}

// instanceRefs walks an instance's field bytes across its superclass chain,
// appending the object ids in its reference fields to out (plus the class
// object itself, which an instance keeps alive). complete is false when a
// class in the chain has no layout yet.
func (st *graphState) instanceRefs(classID uint64, fields []byte, out []uint64) ([]uint64, bool) {
	out = append(out, classID)
	offset := 0
	for c := classID; c != 0; {
		layout, ok := st.layouts[c]
		if !ok {
			return out, false
		}
		for _, typ := range layout.fieldTypes {
			size := int(hprofTypeSize(typ, st.idSize))
			if offset+size > len(fields) {
				return out, true // truncated / mismatched; stop safely
			}
			if typ == hprofTypeObject {
				out = append(out, readID(fields[offset:offset+size], st.idSize))
			}
			offset += size
		}
		c = layout.superID
	}
	return out, true
}

func readID(b []byte, idSize int) uint64 {
	if idSize == 4 {
		return uint64(binary.BigEndian.Uint32(b[:4]))
	}
	return binary.BigEndian.Uint64(b[:8])
}
