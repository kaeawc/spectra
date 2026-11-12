package hostwatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/store"
)

type probeHarness struct {
	svc  *Service
	log  *logger.Capture
	now  time.Time
	fail bool
}

func newProbeHarness(t *testing.T) *probeHarness {
	t.Helper()
	return newProbeHarnessStore(t, testDB(t))
}

func newProbeHarnessStore(t *testing.T, db SampleStore) *probeHarness {
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
	h.svc = NewService(ServiceOptions{Store: db, Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Logger: h.log, Collector: &sequenceCollector{}})
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

type updateRecorder struct {
	mu      sync.Mutex
	details []string
}

func (u *updateRecorder) add(method string, v any) error {
	if e, ok := v.(Event); ok && method == "alerts.event" && e.Type == "updated" {
		u.mu.Lock()
		u.details = append(u.details, e.Alert.Detail)
		u.mu.Unlock()
	}
	return nil
}

func (u *updateRecorder) waitFor(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		u.mu.Lock()
		got := append([]string(nil), u.details...)
		u.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	t.Fatalf("got %d updated events, want %d: %v", len(u.details), n, u.details)
	return nil
}

// Detail-only changes must still reach subscribers when the stale note is added
// and when it is cleared, and the cleared detail is stored immediately.
func TestSustainedProbeNoteEmitsUpdatedEvents(t *testing.T) {
	h := newProbeHarness(t)
	rec := &updateRecorder{}
	stop := make(chan struct{})
	defer close(stop)
	h.svc.subscribe(stop, rec.add, false)
	for m := 0; m <= 20; m++ {
		h.tick(m)
	}
	h.fail = true
	for m := 21; m <= 21+sustainedProbeFailLimit; m++ {
		h.tick(m)
	}
	got := rec.waitFor(t, 1)
	if len(got) != 1 || !strings.Contains(got[0], "probe unavailable") {
		t.Fatalf("flag events: %v", got)
	}
	h.fail = false
	h.tick(40)
	got = rec.waitFor(t, 2)
	if len(got) != 2 || strings.Contains(got[1], "probe unavailable") {
		t.Fatalf("recovery events: %v", got)
	}
	rows, err := h.svc.opts.Store.ListAlerts(context.Background(), "firing", 10)
	if err != nil || len(rows) != 1 || strings.Contains(rows[0].Detail, "probe unavailable") {
		t.Fatalf("cleared detail not stored immediately: %v %+v", err, rows)
	}
}

type flakyStore struct {
	SampleStore
	failNext int
}

func (f *flakyStore) UpsertAlert(ctx context.Context, row store.AlertRow) error {
	if f.failNext > 0 && strings.Contains(row.Detail, "probe unavailable") {
		f.failNext--
		return errors.New("disk full")
	}
	return f.SampleStore.UpsertAlert(ctx, row)
}

// A failed write of the flagged detail is retried on later failed ticks.
func TestSustainedProbeNoteWriteRetried(t *testing.T) {
	fs := &flakyStore{SampleStore: testDB(t)}
	h := newProbeHarnessStore(t, fs)
	for m := 0; m <= 20; m++ {
		h.tick(m)
	}
	fs.failNext = 1
	h.fail = true
	for m := 21; m <= 21+sustainedProbeFailLimit; m++ {
		h.tick(m)
	}
	h.tick(40)
	rows, err := fs.ListAlerts(context.Background(), "firing", 10)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Detail, "probe unavailable") {
		t.Fatalf("flag never persisted after a failed write: %v %+v", err, rows)
	}
}
