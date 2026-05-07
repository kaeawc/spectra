package jvm

import (
	"fmt"
	"strconv"
	"strings"
)

// GCStats holds the GC counter snapshot returned by `jstat -gc <pid>`.
// Column names follow jstat's header row with underscores instead of spaces.
// Zero values indicate the column was absent (e.g. CMS vs G1 have different sets).
type GCStats struct {
	// Survivor spaces
	S0C float64 `json:"s0c"` // Survivor 0 capacity (KiB)
	S1C float64 `json:"s1c"` // Survivor 1 capacity (KiB)
	S0U float64 `json:"s0u"` // Survivor 0 utilization (KiB)
	S1U float64 `json:"s1u"` // Survivor 1 utilization (KiB)

	// Eden / Old
	EC float64 `json:"ec"` // Eden capacity (KiB)
	EU float64 `json:"eu"` // Eden utilization (KiB)
	OC float64 `json:"oc"` // Old generation capacity (KiB)
	OU float64 `json:"ou"` // Old generation utilization (KiB)

	// Metaspace
	MC   float64 `json:"mc"`   // Metaspace capacity (KiB)
	MU   float64 `json:"mu"`   // Metaspace utilization (KiB)
	CCSC float64 `json:"ccsc"` // Compressed class space capacity (KiB)
	CCSU float64 `json:"ccsu"` // Compressed class space utilization (KiB)

	// GC event counts
	YGC  int64   `json:"ygc"`  // Young GC event count
	YGCT float64 `json:"ygct"` // Young GC time (seconds)
	FGC  int64   `json:"fgc"`  // Full GC event count
	FGCT float64 `json:"fgct"` // Full GC time (seconds)
	GCT  float64 `json:"gct"`  // Total GC time (seconds)
}

// ClassStats holds the class-loading counters returned by `jstat -class <pid>`.
type ClassStats struct {
	Loaded        int64   `json:"loaded"`          // loaded class count
	LoadedKiB     float64 `json:"loaded_kib"`      // bytes column, reported by jstat as KiB
	Unloaded      int64   `json:"unloaded"`        // unloaded class count
	UnloadedKiB   float64 `json:"unloaded_kib"`    // bytes column, reported by jstat as KiB
	ClassLoadTime float64 `json:"class_load_time"` // seconds spent loading/unloading classes
}

// CollectGCStats runs `jstat -gc <pid>` once and returns the current GC counters.
// Pass nil for run to use the default system runner.
func CollectGCStats(pid int, run CmdRunner) (*GCStats, error) {
	if run == nil {
		run = DefaultRunner
	}
	out, err := run("jstat", "-gc", fmt.Sprint(pid))
	if err != nil {
		return nil, fmt.Errorf("jstat: %w", err)
	}
	return parseGCStats(string(out))
}

// CollectClassStats runs `jstat -class <pid>` once and returns class counters.
func CollectClassStats(pid int, run CmdRunner) (*ClassStats, error) {
	if run == nil {
		run = DefaultRunner
	}
	out, err := run("jstat", "-class", fmt.Sprint(pid))
	if err != nil {
		return nil, fmt.Errorf("jstat class: %w", err)
	}
	return parseClassStats(string(out))
}

func parseClassStats(out string) (*ClassStats, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("jstat class: unexpected output (got %d lines)", len(lines))
	}
	headers := strings.Fields(lines[0])
	values := strings.Fields(lines[len(lines)-1])
	if len(headers) != len(values) {
		return nil, fmt.Errorf("jstat class: header/value count mismatch (%d vs %d)", len(headers), len(values))
	}
	colF := func(name string) float64 {
		return colFOccurrence(headers, values, name, 1)
	}
	colF2 := func(name string) float64 {
		return colFOccurrence(headers, values, name, 2)
	}
	colI := func(name string) int64 {
		for i, h := range headers {
			if strings.EqualFold(h, name) {
				n, _ := strconv.ParseInt(values[i], 10, 64)
				return n
			}
		}
		return 0
	}
	return &ClassStats{
		Loaded:        colI("Loaded"),
		LoadedKiB:     colF("Bytes"),
		Unloaded:      colI("Unloaded"),
		UnloadedKiB:   colF2("Bytes"),
		ClassLoadTime: colF("Time"),
	}, nil
}

func colFOccurrence(headers, values []string, name string, occurrence int) float64 {
	seen := 0
	for i, h := range headers {
		if strings.EqualFold(h, name) {
			seen++
			if seen == occurrence {
				f, _ := strconv.ParseFloat(values[i], 64)
				return f
			}
		}
	}
	return 0
}

func parseGCStats(out string) (*GCStats, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("jstat: unexpected output (got %d lines)", len(lines))
	}

	headers := strings.Fields(lines[0])
	values := strings.Fields(lines[len(lines)-1]) // last non-empty line is the data row
	if len(headers) != len(values) {
		return nil, fmt.Errorf("jstat: header/value count mismatch (%d vs %d)", len(headers), len(values))
	}

	colF := func(name string) float64 {
		for i, h := range headers {
			if strings.EqualFold(h, name) {
				f, _ := strconv.ParseFloat(values[i], 64)
				return f
			}
		}
		return 0
	}
	colI := func(name string) int64 {
		for i, h := range headers {
			if strings.EqualFold(h, name) {
				n, _ := strconv.ParseInt(values[i], 10, 64)
				return n
			}
		}
		return 0
	}

	return &GCStats{
		S0C:  colF("S0C"),
		S1C:  colF("S1C"),
		S0U:  colF("S0U"),
		S1U:  colF("S1U"),
		EC:   colF("EC"),
		EU:   colF("EU"),
		OC:   colF("OC"),
		OU:   colF("OU"),
		MC:   colF("MC"),
		MU:   colF("MU"),
		CCSC: colF("CCSC"),
		CCSU: colF("CCSU"),
		YGC:  colI("YGC"),
		YGCT: colF("YGCT"),
		FGC:  colI("FGC"),
		FGCT: colF("FGCT"),
		GCT:  colF("GCT"),
	}, nil
}
