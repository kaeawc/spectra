package rules

import (
	"testing"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/sysinfo"
)

func TestSnapshotActivationFDLimit(t *testing.T) {
	s := snapshot.Snapshot{
		FDLimit: sysinfo.FDLimit{Soft: 256, HardUnlimited: true},
	}
	act := SnapshotActivation(s)

	raw, ok := act["fd_limit"]
	if !ok {
		t.Fatal("fd_limit key missing from activation")
	}
	fd, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("fd_limit is %T, want map[string]any", raw)
	}
	if fd["soft"] != 256 {
		t.Errorf("soft = %v, want 256", fd["soft"])
	}
	if fd["hard"] != 0 {
		t.Errorf("hard = %v, want 0", fd["hard"])
	}
	if fd["hard_unlimited"] != true {
		t.Errorf("hard_unlimited = %v, want true", fd["hard_unlimited"])
	}
}

func TestSnapshotActivationFDLimitBothNumeric(t *testing.T) {
	s := snapshot.Snapshot{
		FDLimit: sysinfo.FDLimit{Soft: 10240, Hard: 12288},
	}
	fd, ok := SnapshotActivation(s)["fd_limit"].(map[string]any)
	if !ok {
		t.Fatal("fd_limit missing or wrong type")
	}
	if fd["soft"] != 10240 || fd["hard"] != 12288 || fd["hard_unlimited"] != false {
		t.Errorf("fd_limit = %+v, want soft=10240 hard=12288 hard_unlimited=false", fd)
	}
}
