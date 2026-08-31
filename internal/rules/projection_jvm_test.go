package rules

import (
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestSnapshotActivationProjectsJVMGCCount(t *testing.T) {
	snap := snapshot.Snapshot{
		JVMs: []jvm.Info{{
			PID: 100,
			GC:  &jvm.GCStats{YGC: 12, FGC: 3, OC: 1000, OU: 900},
		}},
	}
	act := SnapshotActivation(snap)
	jvms, ok := act["jvms"].([]any)
	if !ok || len(jvms) != 1 {
		t.Fatalf("jvms projection = %#v", act["jvms"])
	}
	j := jvms[0].(map[string]any)
	// jvm.gc_count is documented as an available rule input.
	if got := j["gc_count"].(int64); got != 15 {
		t.Fatalf("gc_count = %d, want 15 (ygc 12 + fgc 3)", got)
	}
	gc, ok := j["gc"].(map[string]any)
	if !ok {
		t.Fatalf("gc projection missing/typed %T", j["gc"])
	}
	if gc["fgc"].(int64) != 3 || gc["oc"].(float64) != 1000 {
		t.Fatalf("gc detail = %#v", gc)
	}
}

func TestSnapshotActivationJVMWithoutGC(t *testing.T) {
	snap := snapshot.Snapshot{JVMs: []jvm.Info{{PID: 101}}}
	j := SnapshotActivation(snap)["jvms"].([]any)[0].(map[string]any)
	if j["gc_count"].(int64) != 0 {
		t.Fatalf("gc_count = %v, want 0 when GC absent", j["gc_count"])
	}
	if j["gc"] != nil {
		t.Fatalf("gc = %#v, want nil when GC absent", j["gc"])
	}
}

func TestSnapshotActivationProjectsGCDerivedFields(t *testing.T) {
	snap := snapshot.Snapshot{
		JVMs: []jvm.Info{{
			PID: 100,
			GC: &jvm.GCStats{
				S0C: 64, S1C: 64, S0U: 10, S1U: 0,
				EC: 512, EU: 480,
				OC: 1000, OU: 950,
				MC: 200, MU: 180, CCSC: 40, CCSU: 30,
				YGC: 12, FGC: 3, FGCT: 4.5,
			},
		}},
	}
	j := SnapshotActivation(snap)["jvms"].([]any)[0].(map[string]any)
	gc, ok := j["gc"].(map[string]any)
	if !ok {
		t.Fatalf("gc projection missing/typed %T", j["gc"])
	}
	// Survivor + compressed-class-space counters must now be projected.
	for _, k := range []string{"s0c", "s1c", "s0u", "s1u", "ccsc", "ccsu"} {
		if _, ok := gc[k]; !ok {
			t.Errorf("gc projection missing counter %q", k)
		}
	}
	if got := gc["old_gen_used_pct"].(float64); got != 95 {
		t.Errorf("old_gen_used_pct = %v, want 95 (950/1000)", got)
	}
	if got := gc["metaspace_used_pct"].(float64); got != 90 {
		t.Errorf("metaspace_used_pct = %v, want 90 (180/200)", got)
	}
	if gc["full_gc_count"].(int64) != 3 || gc["full_gc_time_s"].(float64) != 4.5 {
		t.Errorf("full_gc aliases = %v/%v", gc["full_gc_count"], gc["full_gc_time_s"])
	}
	if gc["young_gc_count"].(int64) != 12 {
		t.Errorf("young_gc_count = %v, want 12", gc["young_gc_count"])
	}
}

func TestSnapshotActivationGCDerivedZeroDivision(t *testing.T) {
	snap := snapshot.Snapshot{
		JVMs: []jvm.Info{{PID: 1, GC: &jvm.GCStats{OC: 0, OU: 5, MC: 0, MU: 5}}},
	}
	gc := SnapshotActivation(snap)["jvms"].([]any)[0].(map[string]any)["gc"].(map[string]any)
	if gc["old_gen_used_pct"].(float64) != 0 || gc["metaspace_used_pct"].(float64) != 0 {
		t.Fatalf("derived pcts should be 0 when capacity is 0: %#v", gc)
	}
}

func TestSnapshotActivationProjectsClasses(t *testing.T) {
	snap := snapshot.Snapshot{
		JVMs: []jvm.Info{{
			PID:     100,
			Classes: &jvm.ClassStats{Loaded: 4200, Unloaded: 12, LoadedKiB: 8192, ClassLoadTime: 1.25},
		}},
	}
	j := SnapshotActivation(snap)["jvms"].([]any)[0].(map[string]any)
	cls, ok := j["classes"].(map[string]any)
	if !ok {
		t.Fatalf("classes projection missing/typed %T", j["classes"])
	}
	if cls["loaded"].(int64) != 4200 || cls["unloaded"].(int64) != 12 {
		t.Fatalf("classes detail = %#v", cls)
	}

	// Absent Classes projects nil, mirroring gc's absent behavior.
	nocls := SnapshotActivation(snapshot.Snapshot{JVMs: []jvm.Info{{PID: 2}}})
	if nocls["jvms"].([]any)[0].(map[string]any)["classes"] != nil {
		t.Fatal("classes should be nil when absent")
	}
}

func TestSnapshotActivationProjectsHistory(t *testing.T) {
	snap := snapshot.Snapshot{
		JVMs: []jvm.Info{{PID: 100}},
		JVMHistory: snapshot.JVMHistory{
			{PID: 100, OldGenPct: 80, FGC: 1, HeapMB: 512},
			{PID: 100, OldGenPct: 88, FGC: 2, HeapMB: 512},
			{PID: 100, OldGenPct: 96, FGC: 4, HeapMB: 512},
			{PID: 999, OldGenPct: 10}, // unrelated PID must not leak in
		},
	}
	j := SnapshotActivation(snap)["jvms"].([]any)[0].(map[string]any)
	h, ok := j["history"].(map[string]any)
	if !ok {
		t.Fatalf("history projection missing/typed %T", j["history"])
	}
	if h["sample_count"].(int) != 3 {
		t.Errorf("sample_count = %v, want 3", h["sample_count"])
	}
	if h["rising_old_gen"] != true {
		t.Errorf("rising_old_gen = %v, want true (80 -> 96)", h["rising_old_gen"])
	}
	if h["old_gen_pct_last"].(float64) != 96 || h["fgc_last"].(int64) != 4 {
		t.Errorf("history last = %#v", h)
	}

	// No history for the PID -> present map with zero values (null-safe in CEL).
	none := SnapshotActivation(snapshot.Snapshot{JVMs: []jvm.Info{{PID: 7}}})
	nh, ok := none["jvms"].([]any)[0].(map[string]any)["history"].(map[string]any)
	if !ok {
		t.Fatal("history should be a present map even with no samples")
	}
	if nh["sample_count"].(int) != 0 || nh["rising_old_gen"] != false {
		t.Fatalf("empty history = %#v, want sample_count 0 / rising_old_gen false", nh)
	}
}

// TestSnapshotActivationBoundaryKeys locks the top-level external-rule keys so
// a rename that would silently break YAML/CEL rules is caught.
func TestSnapshotActivationBoundaryKeys(t *testing.T) {
	act := SnapshotActivation(snapshot.Snapshot{})
	for _, key := range []string{
		"snapshot", "host", "apps", "processes", "jvms",
		"toolchains", "network", "storage", "power", "sysctls", "fd_limit",
	} {
		if _, ok := act[key]; !ok {
			t.Errorf("projection missing boundary key %q", key)
		}
	}
}
