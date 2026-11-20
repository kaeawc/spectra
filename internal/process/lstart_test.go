package process

import (
	"context"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/proc"
)

func TestCollectAllRunsPSWithUTCAndCLocale(t *testing.T) {
	runner := proc.NewFake().OnExact("ps", []string{"-axwwo", "pid=,ppid=,pcpu=,rss=,vsz=,uid=,user=,lstart=,command="}, proc.Response{
		Result: proc.Result{Stdout: []byte(psFixture)},
	}).OnExact("ps", []string{"-axwwo", "pid=,comm="}, proc.Response{})
	CollectAll(context.Background(), CollectOptions{
		Runner:          runner,
		DetailCollector: func([]Info) map[int]Details { return nil },
	})
	if runner.CallCount() == 0 {
		t.Fatal("no ps calls observed")
	}
	for _, c := range runner.Calls() {
		var tz, lc bool
		for _, kv := range c.Env {
			tz = tz || kv == "TZ=UTC0"
			lc = lc || kv == "LC_ALL=C"
		}
		if !tz || !lc {
			t.Errorf("ps %v env lacks TZ=UTC0/LC_ALL=C", c.Args)
		}
	}
}

// Two processes an hour apart in UTC print identical wall-clock lstart in a
// zone's repeated fall-back hour; reading lstart as UTC keeps them distinct.
func TestParseRowDistinctAcrossFallBackHour(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })

	// 2026-11-01 05:30Z is 01:30 EDT; 06:30Z is 01:30 EST.
	a := parseRow("10 1 0.0 1 1 501 u Sun Nov 1 05:30:00 2026 /bin/a")
	b := parseRow("11 1 0.0 1 1 501 u Sun Nov 1 06:30:00 2026 /bin/b")
	if got := b.StartedAt.Sub(a.StartedAt); got != time.Hour {
		t.Fatalf("delta = %s, want 1h", got)
	}
	if a.StartedAt.Format("15:04") != "01:30" || b.StartedAt.Format("15:04") != "01:30" {
		t.Fatalf("local wall clock = %s, %s; want both 01:30", a.StartedAt, b.StartedAt)
	}
	if a.StartedAt.Location() != loc {
		t.Errorf("location = %v, want host-local", a.StartedAt.Location())
	}
}
