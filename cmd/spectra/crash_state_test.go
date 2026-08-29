package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/sysinfo"
)

func at(s string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", s)
	return t
}

func TestNearestSnapshot(t *testing.T) {
	crash := at("2026-08-05 15:04:00")
	rows := []store.SnapshotRow{
		{ID: "s1", TakenAt: at("2026-08-05 14:00:00")}, // 64m before
		{ID: "s2", TakenAt: at("2026-08-05 14:58:00")}, // 6m before  <- nearest
		{ID: "s3", TakenAt: at("2026-08-05 16:30:00")}, // 86m after
	}
	row, delta, ok := nearestSnapshot(crash, rows, time.Hour)
	if !ok || row.ID != "s2" {
		t.Fatalf("nearest = %q ok=%v, want s2", row.ID, ok)
	}
	if delta != -6*time.Minute {
		t.Errorf("delta = %v, want -6m", delta)
	}
}

func TestNearestSnapshotOutOfWindow(t *testing.T) {
	crash := at("2026-08-05 15:04:00")
	rows := []store.SnapshotRow{{ID: "s1", TakenAt: at("2026-08-05 10:00:00")}}
	if _, _, ok := nearestSnapshot(crash, rows, time.Hour); ok {
		t.Error("expected no snapshot within a 1h window")
	}
}

func ipsWithTime(when string) string {
	return `{"app_name":"MyApp","timestamp":"` + when + `","bug_type":"309","name":"MyApp"}
{"procName":"MyApp","pid":1,"exception":{"type":"EXC_BAD_ACCESS","signal":"SIGSEGV"},"termination":{"indicator":"x"},"faultingThread":0,"threads":[{"triggered":true,"frames":[]}],"usedImages":[]}`
}

func TestRunCrashStateRenders(t *testing.T) {
	deps := crashStateDeps{
		readFile: func(string) ([]byte, error) {
			return []byte(ipsWithTime("2026-08-05 15:04:00.00 -0500")), nil
		},
		loadSnapshots: func(host string) (string, []store.SnapshotRow, error) {
			// crash is 15:04:00 -0500 = 20:04:00 UTC; this snapshot (UTC) is 6m before.
			return "my-mac", []store.SnapshotRow{{ID: "snap-1", TakenAt: at("2026-08-05 19:58:00")}}, nil
		},
		loadSnapshot: func(id string) (*snapshot.Snapshot, error) {
			return &snapshot.Snapshot{
				Power:     sysinfo.PowerState{ThermalPressure: "serious"},
				Processes: []process.Info{{Command: "MyApp", RSSKiB: 1258291}, {Command: "idle", RSSKiB: 1024}},
			}, nil
		},
	}
	var out, errBuf bytes.Buffer
	if code := runCrashStateWithDeps("r.ips", "", time.Hour, false, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"MyApp crashed", "snap-1", "before the crash", "thermal: serious", "correlational"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestRunCrashStateNoSnapshotInWindow(t *testing.T) {
	deps := crashStateDeps{
		readFile: func(string) ([]byte, error) { return []byte(ipsWithTime("2026-08-05 15:04:00.00 -0500")), nil },
		loadSnapshots: func(string) (string, []store.SnapshotRow, error) {
			return "m", []store.SnapshotRow{{ID: "s", TakenAt: at("2020-01-01 00:00:00")}}, nil
		},
		loadSnapshot: func(string) (*snapshot.Snapshot, error) { return &snapshot.Snapshot{}, nil },
	}
	var out, errBuf bytes.Buffer
	if code := runCrashStateWithDeps("r.ips", "", time.Hour, false, &out, &errBuf, deps); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "no snapshot within") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestRunCrashStateBadTimestamp(t *testing.T) {
	deps := crashStateDeps{
		readFile:      func(string) ([]byte, error) { return []byte(ipsWithTime("not-a-time")), nil },
		loadSnapshots: func(string) (string, []store.SnapshotRow, error) { return "m", nil, nil },
		loadSnapshot:  func(string) (*snapshot.Snapshot, error) { return &snapshot.Snapshot{}, nil },
	}
	var out, errBuf bytes.Buffer
	if code := runCrashStateWithDeps("r.ips", "", time.Hour, false, &out, &errBuf, deps); code != 1 {
		t.Fatalf("exit = %d, want 1 for unparseable timestamp", code)
	}
}

func TestRunCrashStateNoArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashState(nil, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
