package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

var nmtBase = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

const nmtArgs = "-Xmx2g -XX:NativeMemoryTracking=summary"

// nmtSnap builds a snapshot with one JVM and len(series[cat]) samples, 5
// minutes apart, the last at nmtBase+5min*(n-1). procStart is the process
// start (zero = unknown).
func nmtSnap(args string, procStart time.Time, series map[string][]int64) snapshot.Snapshot {
	s := baseSnap()
	s.JVMs = []jvm.Info{{PID: 77, MainClass: "leaky.App", VMArgs: args}}
	n := 0
	for _, v := range series {
		n = len(v)
	}
	for i := 0; i < n; i++ {
		cats := map[string]int64{}
		for name, v := range series {
			cats[name] = v[i]
		}
		s.JVMHistory = append(s.JVMHistory, snapshot.JVMSample{
			PID:             77,
			ProcStart:       procStart,
			At:              nmtBase.Add(time.Duration(i) * 5 * time.Minute),
			NMTCommittedKiB: cats,
		})
	}
	return s
}

const mib = 1024

func steady(startMiB, stepMiB int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = (startMiB + int64(i)*stepMiB) * mib
	}
	return out
}

var longAgo = nmtBase.Add(-24 * time.Hour)

func TestNMTGrowthFiresOnSteadyGrowth(t *testing.T) {
	s := nmtSnap(nmtArgs, longAgo, map[string][]int64{
		"Internal": steady(100, 20, 5), // +80 MiB (+80%) over 20 min
		"Thread":   steady(50, 0, 5),   // flat
	})
	f := ruleJVMNativeMemoryGrowth().MatchFn(s)
	if len(f) != 1 {
		t.Fatalf("findings = %v, want exactly one (Internal)", f)
	}
	if f[0].RuleID != "jvm-native-memory-growth" || !strings.Contains(f[0].Subject, "Internal") {
		t.Errorf("finding = %+v", f[0])
	}
	if f[0].Subject != "PID 77 NMT Internal" {
		t.Errorf("subject = %q, want PID + category only", f[0].Subject)
	}
	for _, want := range []string{"leaky.App", "Internal", "80 MiB", "20m0s", "5 samples"} {
		if !strings.Contains(f[0].Message, want) {
			t.Errorf("message %q missing %q", f[0].Message, want)
		}
	}
	for _, want := range []string{"spectra jvm nmt baseline 77", "spectra jvm nmt diff 77"} {
		if !strings.Contains(f[0].Fix, want) {
			t.Errorf("fix %q missing %q", f[0].Fix, want)
		}
	}
}

func TestNMTGrowthQuietWhenLatestSampleHasNoNMT(t *testing.T) {
	s := nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(100, 20, 5)})
	if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 1 {
		t.Fatalf("precondition: want 1 finding, got %v", f)
	}
	// A newer sample whose NMT probe failed (no categories).
	s.JVMHistory = append(s.JVMHistory, snapshot.JVMSample{
		PID: 77, ProcStart: longAgo, At: nmtBase.Add(25 * time.Minute),
	})
	if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 0 {
		t.Fatalf("stale series must not fire without a current reading, got %v", f)
	}
}

func TestNMTGrowthOneFindingPerCategory(t *testing.T) {
	s := nmtSnap(nmtArgs, longAgo, map[string][]int64{
		"Internal": steady(100, 20, 5),
		"Other":    steady(100, 30, 5),
		"Thread":   steady(100, 25, 5),
	})
	if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 3 {
		t.Fatalf("findings = %d, want 3: %v", len(f), f)
	}
}

func TestNMTGrowthQuietCases(t *testing.T) {
	jitter := []int64{100 * mib, 140 * mib, 90 * mib, 150 * mib, 100 * mib}
	cases := map[string]snapshot.Snapshot{
		"flat":            nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(100, 0, 6)}),
		"noisy":           nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": jitter}),
		"small absolute":  nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(10, 5, 5)}),
		"small relative":  nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(2000, 10, 5)}),
		"too few samples": nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(100, 100, 3)}),
		"nmt off":         nmtSnap("-Xmx2g", longAgo, map[string][]int64{"Internal": steady(100, 20, 5)}),
		"nmt off flag":    nmtSnap("-XX:NativeMemoryTracking=off", longAgo, map[string][]int64{"Internal": steady(100, 20, 5)}),
		"java heap only":  nmtSnap(nmtArgs, longAgo, map[string][]int64{"Java Heap": steady(100, 100, 5)}),
		"no history":      func() snapshot.Snapshot { s := nmtSnap(nmtArgs, longAgo, nil); return s }(),
	}
	// Four samples one minute apart: enough samples, span only 3 minutes.
	short := nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(100, 100, 4)})
	for i := range short.JVMHistory {
		short.JVMHistory[i].At = nmtBase.Add(time.Duration(i) * time.Minute)
	}
	cases["short span"] = short

	for name, s := range cases {
		if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 0 {
			t.Errorf("%s: unexpected findings %v", name, f)
		}
	}
}

func TestNMTGrowthToleratesSmallDips(t *testing.T) {
	// 100 -> 200 MiB with a 5 MiB dip (5% of growth, within the 10% noise band).
	series := []int64{100 * mib, 140 * mib, 135 * mib, 170 * mib, 200 * mib}
	s := nmtSnap(nmtArgs, longAgo, map[string][]int64{"Other": series})
	if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 1 {
		t.Fatalf("findings = %v, want 1", f)
	}
}

func TestNMTGrowthWarmupCategories(t *testing.T) {
	grow := map[string][]int64{"Class": steady(100, 40, 5), "Code": steady(100, 40, 5), "Compiler": steady(100, 40, 5)}

	// Younger than the settle period at the last sample: quiet.
	young := nmtSnap(nmtArgs, nmtBase.Add(-5*time.Minute), grow) // age 25m at last sample
	if f := ruleJVMNativeMemoryGrowth().MatchFn(young); len(f) != 0 {
		t.Errorf("young process: unexpected findings %v", f)
	}

	// Unknown start time: cannot prove it settled, quiet.
	unknown := nmtSnap(nmtArgs, time.Time{}, grow)
	if f := ruleJVMNativeMemoryGrowth().MatchFn(unknown); len(f) != 0 {
		t.Errorf("unknown start: unexpected findings %v", f)
	}

	// Settled before the first sample: growth persists past warmup, fires for each.
	old := nmtSnap(nmtArgs, longAgo, grow)
	if f := ruleJVMNativeMemoryGrowth().MatchFn(old); len(f) != 3 {
		t.Errorf("settled process: findings = %d, want 3: %v", len(f), f)
	}

	// Settle point falls mid-window: only post-settle samples count, and the
	// growth within them (3 samples) is too few to fire.
	mid := nmtSnap(nmtArgs, nmtBase.Add(-10*time.Minute), grow) // settles at nmtBase+20m
	if f := ruleJVMNativeMemoryGrowth().MatchFn(mid); len(f) != 0 {
		t.Errorf("mid-window settle: unexpected findings %v", f)
	}
}

func TestNMTGrowthYoungProcessStillFlagsNonWarmup(t *testing.T) {
	s := nmtSnap(nmtArgs, nmtBase.Add(-5*time.Minute), map[string][]int64{"Internal": steady(100, 20, 5)})
	if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 1 {
		t.Fatalf("findings = %v, want 1", f)
	}
}

func TestNMTGrowthConfigurable(t *testing.T) {
	s := nmtSnap(nmtArgs, longAgo, map[string][]int64{"Internal": steady(100, 5, 5)}) // +20 MiB
	if f := ruleJVMNativeMemoryGrowth().MatchFn(s); len(f) != 0 {
		t.Fatalf("defaults should be quiet, got %v", f)
	}
	cfg := DefaultNMTGrowthConfig()
	cfg.MinGrowthKiB = 10 * mib
	cfg.MinGrowthPct = 10
	if f := ruleJVMNativeMemoryGrowthWith(cfg).MatchFn(s); len(f) != 1 {
		t.Fatalf("lowered thresholds should fire, got %v", f)
	}
}

func TestJVMNativeMemoryGrowthInV1Catalog(t *testing.T) {
	for _, r := range V1Catalog() {
		if r.ID == "jvm-native-memory-growth" {
			return
		}
	}
	t.Fatal("jvm-native-memory-growth missing from V1Catalog")
}
