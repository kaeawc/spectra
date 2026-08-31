// Package oom scans log text for java.lang.OutOfMemoryError occurrences and
// classifies them by variant. Each variant has a distinct root cause and fix,
// so classification — not mere detection — is the point.
//
// The package is deliberately free of snapshot/JVM dependencies so it can be
// unit-tested in isolation and reused by any collector.
package oom

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// Variant is a classified OutOfMemoryError kind.
type Variant string

const (
	VariantHeapSpace            Variant = "java_heap_space"
	VariantGCOverhead           Variant = "gc_overhead_limit"
	VariantMetaspace            Variant = "metaspace"
	VariantCompressedClassSpace Variant = "compressed_class_space"
	VariantDirectBuffer         Variant = "direct_buffer_memory"
	VariantNativeThread         Variant = "native_thread"
	VariantArraySize            Variant = "array_size_vm_limit"
	VariantNativeMemory         Variant = "native_memory"
	VariantUnknown              Variant = "unknown"
)

// marker is the fully-qualified exception name that anchors an occurrence.
const marker = "java.lang.OutOfMemoryError"

// Event is one OutOfMemoryError occurrence found in a scanned stream.
type Event struct {
	Variant Variant `json:"variant"`
	Message string  `json:"message"` // text after the marker, trimmed
	LineNo  int     `json:"line_no"` // 1-based line within the scanned stream
}

// Classify maps an OOM message tail to a Variant. Order matters: "compressed
// class space" must be tested before "metaspace" (its message also mentions
// class metadata), and the native-thread phrasing varies across JDKs.
func Classify(msg string) Variant {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "compressed class space"):
		return VariantCompressedClassSpace
	case strings.Contains(m, "metaspace"):
		return VariantMetaspace
	case strings.Contains(m, "java heap space"):
		return VariantHeapSpace
	case strings.Contains(m, "gc overhead limit"):
		return VariantGCOverhead
	case strings.Contains(m, "direct buffer memory"):
		return VariantDirectBuffer
	case strings.Contains(m, "native thread"):
		return VariantNativeThread
	case strings.Contains(m, "requested array size"):
		return VariantArraySize
	case strings.Contains(m, "map failed"), strings.Contains(m, "out of swap"):
		return VariantNativeMemory
	default:
		return VariantUnknown
	}
}

// Human returns a readable label for the variant.
func (v Variant) Human() string {
	switch v {
	case VariantHeapSpace:
		return "Java heap space"
	case VariantGCOverhead:
		return "GC overhead limit exceeded"
	case VariantMetaspace:
		return "Metaspace"
	case VariantCompressedClassSpace:
		return "Compressed class space"
	case VariantDirectBuffer:
		return "Direct buffer memory"
	case VariantNativeThread:
		return "unable to create native thread"
	case VariantArraySize:
		return "Requested array size exceeds VM limit"
	case VariantNativeMemory:
		return "native memory (map failed / out of swap)"
	default:
		return "unknown"
	}
}

// Remediation returns variant-specific first-step guidance.
func (v Variant) Remediation() string {
	switch v {
	case VariantHeapSpace, VariantGCOverhead:
		return "Capture a heap dump/histogram to find the retained live set; raise -Xmx only if the live set is legitimately large."
	case VariantMetaspace, VariantCompressedClassSpace:
		return "Investigate a classloader leak (loaded-class count rising); raise -XX:MaxMetaspaceSize / -XX:CompressedClassSpaceSize if the class count is legitimately high."
	case VariantDirectBuffer:
		return "Audit direct ByteBuffer / NIO usage and set -XX:MaxDirectMemorySize; direct buffers are freed only when their referents are GC'd."
	case VariantNativeThread:
		return "This is thread/native exhaustion, not heap: reduce thread count or raise the OS/process thread limit; check -Xss."
	case VariantArraySize:
		return "A single allocation exceeded the VM array-size limit — usually a bug computing a size or a corrupt length."
	case VariantNativeMemory:
		return "The OS could not satisfy a native mapping — check system memory/swap and native (off-heap) allocations with NMT."
	default:
		return "Inspect the surrounding stack trace to identify the exhausted resource."
	}
}

// Scan reads r line by line and returns one Event per line containing the
// OutOfMemoryError marker. It never returns an error and is safe on arbitrary
// input.
func Scan(r io.Reader) []Event {
	var events []Event
	sc := bufio.NewScanner(r)
	// Allow long lines (stack frames, JSON logs) up to 1 MiB.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		idx := strings.Index(text, marker)
		if idx < 0 {
			continue
		}
		msg := strings.TrimSpace(text[idx+len(marker):])
		msg = strings.TrimSpace(strings.TrimPrefix(msg, ":"))
		events = append(events, Event{Variant: Classify(msg), Message: msg, LineNo: line})
	}
	return events
}

// ScanFile scans the file at path for OOM occurrences. When the file is larger
// than maxBytes (and maxBytes > 0), only its trailing maxBytes are scanned —
// recent OOMs sit near the end of a log, and this bounds the work. A
// missing/unreadable file or a non-regular file returns (nil, err) / (nil, nil)
// so callers can absorb it per the partial-snapshot contract.
//
// When the file is truncated to a tail, LineNo values are relative to that tail,
// not the whole file.
func ScanFile(path string, maxBytes int64) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, nil
	}
	if maxBytes > 0 && fi.Size() > maxBytes {
		if _, err := f.Seek(fi.Size()-maxBytes, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return Scan(f), nil
}
