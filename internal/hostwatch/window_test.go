package hostwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/store"
)

type flakyMetricsStore struct {
	*store.DB
	fail int
}

func (f *flakyMetricsStore) SaveProcessMetrics(ctx context.Context, rows []store.ProcessMetricRow) error {
	if f.fail > 0 {
		f.fail--
		return errors.New("database is locked")
	}
	return f.DB.SaveProcessMetrics(ctx, rows)
}

// A failed process-metrics save leaves the minutes unwritten, so the next
// flush writes them.
func TestMaintenanceRetriesFailedMetricsSave(t *testing.T) {
	db := &flakyMetricsStore{DB: testDB(t), fail: 1}
	m := metrics.NewCollector()
	svc := NewService(ServiceOptions{Store: db, Metrics: m})
	ctx := context.Background()
	m0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.Add(metrics.Sample{TakenAt: m0.Add(10 * time.Second), PID: 42, RSSKiB: 100})
	svc.maintenance(ctx, m0.Add(15*time.Second)) // the first call only arms the timers
	svc.maintenance(ctx, m0.Add(90*time.Second)) // save fails
	if rows, err := db.GetProcessMetrics(ctx, 42, 0); err != nil || len(rows) != 0 {
		t.Fatalf("after failed save: %+v, %v", rows, err)
	}
	m.Add(metrics.Sample{TakenAt: m0.Add(100 * time.Second), PID: 42, RSSKiB: 300})
	svc.maintenance(ctx, m0.Add(160*time.Second)) // save succeeds
	rows, err := db.GetProcessMetrics(ctx, 42, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("after retry: %+v, %v; want minutes 0 and 1", rows, err)
	}
}

// The evaluation window holds two hours of samples and its backing array
// stays proportional to that however long the daemon runs.
func TestWindowStaysBounded(t *testing.T) {
	s := &Service{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const step = 15 * time.Second
	for i := 0; i < 24*3600/int(step/time.Second); i++ { // a day of ticks
		at = at.Add(step)
		s.appendWindowLocked(Sample{At: at})
	}
	want := int(2*time.Hour/step) + 1
	if len(s.window) != want {
		t.Fatalf("window = %d samples, want %d", len(s.window), want)
	}
	if cap(s.window) > 2*want+64 {
		t.Fatalf("window capacity %d for %d samples", cap(s.window), want)
	}
	if !s.window[len(s.window)-1].At.Equal(at) || s.window[0].At.Before(at.Add(-2*time.Hour)) {
		t.Fatalf("window spans %v..%v", s.window[0].At, s.window[len(s.window)-1].At)
	}
}

// parseProcesses keeps only the five busiest processes, highest first,
// without holding a slice of every process.
func TestParseProcessesTopCPUBounded(t *testing.T) {
	var b strings.Builder
	for pid := 1; pid <= 500; pid++ {
		fmt.Fprintf(&b, "%d 1 1024 %d.%d /usr/bin/p%d\n", pid, pid%97, pid%10, pid)
	}
	s := Sample{Kinds: map[string]KindStat{}}
	if n := parseProcesses([]byte(b.String()), nil, &s, nil); n != 500 {
		t.Fatalf("count = %d", n)
	}
	if len(s.TopCPU) != 5 || cap(s.TopCPU) > 5 {
		t.Fatalf("top = %d entries, cap %d", len(s.TopCPU), cap(s.TopCPU))
	}
	// pid%97 == 96 at pids 96, 193, 290, 387 and 484; pid%10 orders them.
	want := []string{"p387", "p96", "p484", "p193", "p290"}
	for i, p := range s.TopCPU {
		if p.Name != want[i] {
			t.Fatalf("top = %+v, want names %v", s.TopCPU, want)
		}
	}
}
