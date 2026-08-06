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
