package rules

import (
	"strings"

	"github.com/kaeawc/spectra/internal/jvm"
)

// VMArgsFacts is the parsed view of jvm.Info.VMArgs.
//
// Sentinel values: -1 means "flag not present in args"; 0 is a valid value.
// Sizes are in bytes; the parser normalizes k/m/g suffixes.
type VMArgsFacts struct {
	XmxBytes int64
	XmsBytes int64

	MaxHeapFreeRatio     int
	MinHeapFreeRatio     int
	MaxMetaspaceFreeRatio int
	MinMetaspaceFreeRatio int

	GCAlgorithm   string
	NMTEnabled    bool
	HeapDumpOnOOM bool

	Raw string
}

// ParseVMArgs builds a VMArgsFacts from a raw VM args string.
// An empty string returns a zero-value facts with all -1 ratios.
func ParseVMArgs(raw string) VMArgsFacts {
	f := VMArgsFacts{
		MaxHeapFreeRatio:      -1,
		MinHeapFreeRatio:      -1,
		MaxMetaspaceFreeRatio: -1,
		MinMetaspaceFreeRatio: -1,
		Raw:                   raw,
	}
	if raw == "" {
		return f
	}
	for _, tok := range strings.Fields(raw) {
		switch {
		case strings.HasPrefix(tok, "-Xmx"):
			f.XmxBytes = parseSizeSuffix(tok[len("-Xmx"):])
		case strings.HasPrefix(tok, "-Xms"):
			f.XmsBytes = parseSizeSuffix(tok[len("-Xms"):])
		case strings.HasPrefix(tok, "-XX:MaxHeapFreeRatio="):
			f.MaxHeapFreeRatio = atoiOrNeg1(tok[len("-XX:MaxHeapFreeRatio="):])
		case strings.HasPrefix(tok, "-XX:MinHeapFreeRatio="):
			f.MinHeapFreeRatio = atoiOrNeg1(tok[len("-XX:MinHeapFreeRatio="):])
		case strings.HasPrefix(tok, "-XX:MaxMetaspaceFreeRatio="):
			f.MaxMetaspaceFreeRatio = atoiOrNeg1(tok[len("-XX:MaxMetaspaceFreeRatio="):])
		case strings.HasPrefix(tok, "-XX:MinMetaspaceFreeRatio="):
			f.MinMetaspaceFreeRatio = atoiOrNeg1(tok[len("-XX:MinMetaspaceFreeRatio="):])
		case strings.HasPrefix(tok, "-XX:NativeMemoryTracking="):
			v := tok[len("-XX:NativeMemoryTracking="):]
			f.NMTEnabled = v == "summary" || v == "detail"
		case tok == "-XX:+HeapDumpOnOutOfMemoryError":
			f.HeapDumpOnOOM = true
		default:
			if gc := gcAlgorithmFor(tok); gc != "" {
				f.GCAlgorithm = gc
			}
		}
	}
	return f
}

var gcFlagToAlgorithm = map[string]string{
	"-XX:+UseSerialGC":     "Serial",
	"-XX:+UseParallelGC":   "Parallel",
	"-XX:+UseG1GC":         "G1",
	"-XX:+UseZGC":          "Z",
	"-XX:+UseShenandoahGC": "Shenandoah",
	"-XX:+UseEpsilonGC":    "Epsilon",
}

func gcAlgorithmFor(tok string) string { return gcFlagToAlgorithm[tok] }

// parseSizeSuffix interprets a size with optional k/m/g/t suffix and returns bytes.
// Returns 0 on parse failure.
func parseSizeSuffix(raw string) int64 {
	if raw == "" {
		return 0
	}
	mult := int64(1)
	last := raw[len(raw)-1]
	digits := raw
	switch last {
	case 'k', 'K':
		mult = 1024
		digits = raw[:len(raw)-1]
	case 'm', 'M':
		mult = 1024 * 1024
		digits = raw[:len(raw)-1]
	case 'g', 'G':
		mult = 1024 * 1024 * 1024
		digits = raw[:len(raw)-1]
	case 't', 'T':
		mult = 1024 * 1024 * 1024 * 1024
		digits = raw[:len(raw)-1]
	}
	n := int64(atoi(digits))
	if n == 0 {
		return 0
	}
	return n * mult
}

func atoiOrNeg1(s string) int {
	if s == "" {
		return -1
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
	}
	return atoi(s)
}

// XmxMB returns the parsed Xmx heap ceiling in MiB, or 0 if not set.
// Convenience for callers that want the legacy parseXmxMB behavior.
func (f VMArgsFacts) XmxMB() int64 {
	if f.XmxBytes <= 0 {
		return 0
	}
	return f.XmxBytes / (1024 * 1024)
}

// FactsFor builds a VMArgsFacts directly from a jvm.Info.
func FactsFor(j jvm.Info) VMArgsFacts { return ParseVMArgs(j.VMArgs) }
