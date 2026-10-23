package hostwatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/proc"
)

type probeHarness struct {
	svc  *Service
	log  *logger.Capture
	now  time.Time
	fail bool
}

func newProbeHarness(t *testing.T) *probeHarness {
	t.Helper()
	h := &probeHarness{log: logger.NewCapture(slog.LevelDebug)}
	start := svcT0.Add(-5 * time.Hour)
	inner := &scriptRunner{sampleOut: "x", ps: func() string {
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n", fmtEtime(h.now.Sub(start)))
	}}
	r := runnerFunc(func(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
		if h.fail && cmd.Name == "ps" {
			return proc.Result{ExitCode: 1}, errors.New("ps failed")
		}
		return inner.Run(ctx, cmd)
	})
	h.svc = NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Logger: h.log, Collector: &sequenceCollector{}})
	return h
}

func (h *probeHarness) tick(m int) {
	h.now = minutes(m)
	h.svc.TickSustained(context.Background(), h.now)
}

func TestSustainedPersistentProbeFailureIsFlagged(t *testing.T) {
	h := newProbeHarness(t)
	for m := 0; m <= 20; m++ {
		h.tick(m)
	}
	before := firingKeys(t, h.svc)
	if len(before) != 1 || strings.Contains(before[0].Detail, "probe unavailable") {
		t.Fatalf("setup: %+v", before)
	}
	h.fail = true
	for m := 21; m < 21+sustainedProbeFailLimit-1; m++ {
		h.tick(m)
	}
	if got := firingKeys(t, h.svc); strings.Contains(got[0].Detail, "probe unavailable") {
		t.Fatalf("flagged before %d consecutive failures: %s", sustainedProbeFailLimit, got[0].Detail)
	}
	if len(h.log.FilterLevel(slog.LevelWarn)) != 0 {
		t.Fatal("warned before the failure limit")
	}
	h.tick(21 + sustainedProbeFailLimit - 1)
	got := firingKeys(t, h.svc)
	if len(got) != 1 || got[0].ID != before[0].ID || !strings.Contains(got[0].Detail, "probe unavailable") {
		t.Fatalf("alert not flagged (and kept) after %d failures: %+v", sustainedProbeFailLimit, got)
	}
	if len(h.log.FilterLevel(slog.LevelWarn)) == 0 {
		t.Fatal("no warning logged at the failure limit")
	}
	rows, err := h.svc.opts.Store.ListAlerts(context.Background(), "firing", 10)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Detail, "probe unavailable") {
		t.Fatalf("flag not persisted: %v %+v", err, rows)
	}
	// Repeated failures must not duplicate the note.
	h.tick(40)
	if d := firingKeys(t, h.svc)[0].Detail; strings.Count(d, "probe unavailable") != 1 {
		t.Fatalf("note duplicated: %s", d)
	}
	// Recovery clears the note and keeps the ID.
	h.fail = false
	h.tick(41)
	got = firingKeys(t, h.svc)
	if len(got) != 1 || got[0].ID != before[0].ID || strings.Contains(got[0].Detail, "probe unavailable") {
		t.Fatalf("flag not cleared on recovery: %+v", got)
	}
}

func TestSustainedProbeFailureCounterResetsOnSuccess(t *testing.T) {
	h := newProbeHarness(t)
	for m := 0; m <= 20; m++ {
		h.tick(m)
	}
	m := 21
	for round := 0; round < 3; round++ {
		h.fail = true
		for i := 0; i < sustainedProbeFailLimit-1; i++ {
			h.tick(m)
			m++
		}
		h.fail = false
		h.tick(m)
		m++
	}
	if got := firingKeys(t, h.svc); len(got) != 1 || strings.Contains(got[0].Detail, "probe unavailable") {
		t.Fatalf("intermittent failures flagged: %+v", got)
	}
}
