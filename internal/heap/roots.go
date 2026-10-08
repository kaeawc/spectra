package heap

import "sort"

// RootKind names why the JVM treats an object as a GC root.
type RootKind string

const (
	RootUnknown          RootKind = "unknown"
	RootJNIGlobal        RootKind = "jni_global"
	RootJNILocal         RootKind = "jni_local"
	RootJavaFrame        RootKind = "java_frame"
	RootNativeStack      RootKind = "native_stack"
	RootStickyClass      RootKind = "sticky_class"
	RootThreadBlock      RootKind = "thread_block"
	RootMonitorUsed      RootKind = "monitor_used"
	RootThreadObject     RootKind = "thread_object"
	RootInternedString   RootKind = "interned_string"
	RootFinalizing       RootKind = "finalizing"
	RootDebugger         RootKind = "debugger"
	RootReferenceCleanup RootKind = "reference_cleanup"
	RootVMInternal       RootKind = "vm_internal"
	RootJNIMonitor       RootKind = "jni_monitor"
)

// GCRoot is one GC-root record. ThreadID is the thread object owning a
// thread-scoped root (Java frame, JNI local, native stack, thread block, JNI
// monitor) when the dump identifies it, else 0.
type GCRoot struct {
	Object   int32    `json:"-"`
	Kind     RootKind `json:"kind"`
	ThreadID uint64   `json:"thread_id,omitempty"`
}

type rawRoot struct {
	id           uint64
	kind         RootKind
	threadSerial uint32
}

// rootRecordLayout maps a ROOT_* heap-dump sub-record tag to its kind and total
// body size (after the tag). The first field is always the rooted object id.
func rootRecordLayout(idSize int, tag byte) (RootKind, int64, bool) {
	id := int64(idSize)
	switch tag {
	case hprofRootUnknown:
		return RootUnknown, id, true
	case hprofRootStickyClass:
		return RootStickyClass, id, true
	case hprofRootMonitorUsed:
		return RootMonitorUsed, id, true
	case hprofRootInterned:
		return RootInternedString, id, true
	case hprofRootFinalizing:
		return RootFinalizing, id, true
	case hprofRootDebugger:
		return RootDebugger, id, true
	case hprofRootRefCleanup:
		return RootReferenceCleanup, id, true
	case hprofRootVMInternal:
		return RootVMInternal, id, true
	case hprofRootJNIGlobal:
		return RootJNIGlobal, id + id, true
	case hprofRootNativeStack:
		return RootNativeStack, id + 4, true
	case hprofRootThreadBlock:
		return RootThreadBlock, id + 4, true
	case hprofRootJNILocal:
		return RootJNILocal, id + 8, true
	case hprofRootJavaFrame:
		return RootJavaFrame, id + 8, true
	case hprofRootThreadObject:
		return RootThreadObject, id + 8, true
	case hprofRootJNIMonitor:
		return RootJNIMonitor, id + 8, true
	default:
		return "", 0, false
	}
}

// hasThreadSerial reports whether a root record's second field is a thread
// serial number.
func hasThreadSerial(kind RootKind) bool {
	switch kind {
	case RootJNILocal, RootJavaFrame, RootNativeStack, RootThreadBlock, RootThreadObject, RootJNIMonitor:
		return true
	default:
		return false
	}
}

func (st *graphState) readRoot(sub *hprofReader, kind RootKind, totalBytes int64) error {
	objID, err := sub.id()
	if err != nil {
		return err
	}
	rest := totalBytes - int64(sub.idSize)
	var serial uint32
	if hasThreadSerial(kind) {
		if serial, err = sub.u4(); err != nil {
			return err
		}
		rest -= 4
	}
	if kind == RootThreadObject && objID != 0 {
		st.b.threadBySerial[serial] = objID
	}
	if objID != 0 {
		st.b.roots = append(st.b.roots, rawRoot{id: objID, kind: kind, threadSerial: serial})
	}
	return sub.skip(rest)
}

// resolveRoots keeps the root records whose object is in the dump, sorted by
// object, and the deduplicated rooted-object set the dominator tree starts
// from (in first-seen order).
func (g *ObjectGraph) resolveRoots(raw []rawRoot, threadBySerial map[uint32]uint64) {
	seen := map[int32]bool{}
	for _, r := range raw {
		i, ok := g.Index(r.id)
		if !ok {
			continue
		}
		root := GCRoot{Object: i, Kind: r.kind}
		if hasThreadSerial(r.kind) && r.kind != RootThreadObject {
			root.ThreadID = threadBySerial[r.threadSerial]
		}
		g.gcRoots = append(g.gcRoots, root)
		if !seen[i] {
			seen[i] = true
			g.roots = append(g.roots, i)
		}
	}
	sort.SliceStable(g.gcRoots, func(a, b int) bool { return g.gcRoots[a].Object < g.gcRoots[b].Object })
}

// GCRoots returns every GC-root record whose object is in the dump.
func (g *ObjectGraph) GCRoots() []GCRoot { return g.gcRoots }

// RootsOf returns the GC-root records for object i (empty if it is not a
// root). The slice aliases the graph's storage.
func (g *ObjectGraph) RootsOf(i int32) []GCRoot {
	lo := sort.Search(len(g.gcRoots), func(k int) bool { return g.gcRoots[k].Object >= i })
	hi := lo
	for hi < len(g.gcRoots) && g.gcRoots[hi].Object == i {
		hi++
	}
	return g.gcRoots[lo:hi]
}
