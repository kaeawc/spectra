package hostwatch

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idgen"
)

// restartAfterBusy fires an alert for a process started five hours before
// svcT0, restarts the service at minute 21, and ticks three more minutes with
// ps reporting restartedStart as the process start time.
func restartAfterBusy(t *testing.T, restartedStart time.Time) (first, second *Service) {
	t.Helper()
	var now time.Time
	started := svcT0.Add(-5 * time.Hour)
	r := &scriptRunner{sampleOut: "x", ps: func() string {
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n", fmtEtime(now.Sub(started)))
	}}
	first = NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	for m := 0; m <= 20; m++ {
		now = minutes(m)
		first.TickSustained(context.Background(), now)
	}
	if got := firingKeys(t, first); len(got) != 1 {
		t.Fatalf("setup: %+v", got)
	}
	started = restartedStart
	second, _ = restartedService(t, first, r, minutes(21))
	for m := 21; m <= 23; m++ {
		now = minutes(m)
		second.TickSustained(context.Background(), now)
	}
	return first, second
}

// The since-time shown after a restart is the original one, not FiredAt.
func TestSustainedRestoredAlertKeepsOriginalSince(t *testing.T) {
	first, second := restartAfterBusy(t, svcT0.Add(-5*time.Hour))
	orig := firingKeys(t, first)[0]
	got := firingKeys(t, second)
	if len(got) != 1 || got[0].ID != orig.ID {
		t.Fatalf("alert not carried: %+v", got)
	}
	want := "since " + svcT0.UTC().Format(time.RFC3339) + ";"
	if !strings.Contains(got[0].Detail, want) {
		t.Fatalf("detail lost original since %q (FiredAt %v): %s", want, orig.FiredAt, got[0].Detail)
	}
}

// A restart that reuses the PID for a same-named process with a different
// start time must not adopt the old alert.
func TestSustainedRestoredAlertNotAdoptedByReusedPID(t *testing.T) {
	_, second := restartAfterBusy(t, svcT0.Add(11*time.Minute))
	for m := 24; m <= 30; m++ {
		second.TickSustained(context.Background(), minutes(m))
	}
	if got := firingKeys(t, second); len(got) != 0 {
		t.Fatalf("reused PID adopted the old alert: %+v", got)
	}
}

// Alerts stored before identity was recorded parse to zero values, so they
// restore by name and FiredAt as before.
func TestSustainedLegacyDetailHasNoIdentity(t *testing.T) {
	parsed := parseRestoredIdent("diagnosticd (pid 412) averaged 90% CPU for 20m0s; up 5h0m0s; next: x")
	if !parsed.since.IsZero() || !parsed.started.IsZero() {
		t.Fatalf("legacy detail parsed: %+v", parsed)
	}
}

func TestSustainedDetailRoundTripsIdentity(t *testing.T) {
	f := SustainedFinding{PID: 412, Name: "/usr/libexec/diagnosticd", Started: svcT0.Add(-5 * time.Hour), Since: svcT0.Add(-20 * time.Minute), Duration: 20 * time.Minute, AvgCPU: 90}
	got := parseRestoredIdent(SustainedDetail(f, svcT0, nil, "darwin", ""))
	if !got.since.Equal(f.Since) || !got.started.Equal(f.Started) {
		t.Fatalf("round trip: %+v", got)
	}
	flagged := parseRestoredIdent(SustainedDetail(f, svcT0, nil, "darwin", "") + probeUnavailableNote)
	if !flagged.since.Equal(f.Since) || !flagged.started.Equal(f.Started) {
		t.Fatalf("flagged round trip: %+v", flagged)
	}
}
