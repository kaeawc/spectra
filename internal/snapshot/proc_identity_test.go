package snapshot

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func TestProcessStart(t *testing.T) {
	start := time.Date(2026, 5, 8, 10, 0, 0, 750_000_000, time.FixedZone("x", 3600))
	want := time.Date(2026, 5, 8, 9, 0, 0, 0, time.UTC)
	cases := map[string]process.Info{
		"start time":          {StartTime: start},
		"started at fallback": {StartedAt: start},
	}
	for name, p := range cases {
		if got := ProcessStart(p); !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("%s: ProcessStart = %v, want %v UTC", name, got, want)
		}
	}
	if got := ProcessStart(process.Info{}); !got.IsZero() {
		t.Errorf("unknown start: ProcessStart = %v, want zero", got)
	}
}

func TestProcessStartsByPIDSkipsUnknown(t *testing.T) {
	start := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	got := ProcessStartsByPID([]process.Info{{PID: 1, StartTime: start}, {PID: 2}})
	if len(got) != 1 || !got[1].Equal(start) {
		t.Fatalf("ProcessStartsByPID = %v, want only pid 1", got)
	}
}

func TestFDSampleFromCarriesProcessStart(t *testing.T) {
	start := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	sm, ok := FDSampleFrom(process.Info{PID: 7, StartTime: start, OpenFDs: 3}, start.Add(time.Hour))
	if !ok || !sm.ProcStart.Equal(start) {
		t.Fatalf("FDSampleFrom = (%+v, %v), want ProcStart %v", sm, ok, start)
	}
}
