package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/process"
)

var updateReportGolden = flag.Bool("update-report-golden", false, "rewrite testdata/report_golden/*.json")

// checkReportGolden pins a command's --json bytes, so moving its report
// builder out of package main cannot change what scripts parse.
func checkReportGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "report_golden", name+".json")
	if *updateReportGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-report-golden): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s --json changed:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestReportGoldenLoad(t *testing.T) {
	fixed := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		deps loadDeps
		args []string
	}{
		"load":          {fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n"), []string{"--json", "--root", "/r"}},
		"load_unknown":  {fakeLoadDeps("6.00 1 1 1/1 1", ""), []string{"--json", "--top", "1"}},
		"load_indexing": {indexingDeps(), []string{"--json", "--root", "/r"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.deps.Now = func() time.Time { return fixed }
			var out, errb bytes.Buffer
			runLoadWithIO(tc.args, &out, &errb, tc.deps)
			if errb.Len() > 0 {
				t.Fatalf("stderr: %s", errb.String())
			}
			checkReportGolden(t, name, out.Bytes())
		})
	}
}

func TestReportGoldenOrphans(t *testing.T) {
	var out, errb bytes.Buffer
	runOrphansWithIO([]string{"--json", "--root", "/tmp/work"}, &out, &errb, orphansFakeDeps())
	checkReportGolden(t, "orphans", out.Bytes())
	out.Reset()
	runOrphansWithIO([]string{"--json", "--no-daemon", "--root", "/nowhere"}, &out, &errb, orphansFakeDeps())
	checkReportGolden(t, "orphans_empty", out.Bytes())
}

func TestReportGoldenIdle(t *testing.T) {
	deps, _ := fakeIdleDeps(map[int]float64{20: 0, 21: 0.1})
	var out, errb bytes.Buffer
	runIdleWithIO([]string{"--json", "--root", "/work/lane", "--pids", "21,555"}, &out, &errb, deps)
	checkReportGolden(t, "idle", out.Bytes())
}

func TestReportGoldenOwner(t *testing.T) {
	started := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	holder := owner.Holder{PID: 20, User: "jason", Command: "app", Hold: owner.HoldListen, Detail: "tcp *:8080",
		Started: started, Chain: []owner.Link{{PID: 1, Command: "launchd"}}}
	find := func(_ context.Context, tg owner.Target) (owner.Result, error) {
		return owner.Result{Target: tg, Holders: []owner.Holder{holder}}, nil
	}
	var out, errb bytes.Buffer
	runOwnerWithIO([]string{"--json", ":8080"}, &out, &errb, "/work", find, nil)
	checkReportGolden(t, "owner", out.Bytes())
	under := func(context.Context, []string) (owner.UnderResult, error) {
		return owner.UnderResult{Incomplete: true, Hint: "denied"}, nil
	}
	out.Reset()
	runOwnerWithIO([]string{"--json", "--under", "repo", "/x"}, &out, &errb, "/work", nil, under)
	checkReportGolden(t, "owner_under", out.Bytes())
}

func TestReportGoldenLineage(t *testing.T) {
	var out, errb bytes.Buffer
	runLineageWithIO([]string{"--json", "10"}, &out, &errb, lineageFakeProcs())
	checkReportGolden(t, "lineage", out.Bytes())
}

func TestReportGoldenSpawns(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var out, errb bytes.Buffer
	runSpawnsWithIO(context.Background(), []string{"--interval", "1s", "--duration", "3s", "--json"}, &out, &errb, goldenSpawnsDeps(t0))
	checkReportGolden(t, "spawns", out.Bytes())
}

func goldenSpawnsDeps(t0 time.Time) spawnsDeps {
	clk := clock.NewFake(t0)
	ticks := make(chan time.Time, 10)
	for i := 0; i < 10; i++ {
		ticks <- t0
	}
	base := []process.Info{
		{PID: 1, Command: "launchd", StartTime: t0.Add(-time.Hour)},
		{PID: 100, PPID: 1, Command: "zsh", StartTime: t0.Add(-time.Minute)},
	}
	child := append(append([]process.Info{}, base...), process.Info{PID: 200, PPID: 100, Command: "make", StartTime: t0})
	tables := [][]process.Info{base, child, base}
	n := 0
	return spawnsDeps{
		Collect: func(context.Context) []process.Info {
			clk.Advance(time.Second)
			i := n
			n++
			return tables[i%len(tables)]
		},
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		Clock:  clk,
	}
}
