package hostwatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/spawnorigin"
)

type cwdSpawnBackend struct {
	fakeSpawnBackend
	cwds      map[int]string
	cwdCalls  int
	lastBatch []int
}

func (c *cwdSpawnBackend) Cwds(_ context.Context, pids []int) map[int]string {
	c.cwdCalls++
	c.lastBatch = pids
	out := map[int]string{}
	for _, pid := range pids {
		if d, ok := c.cwds[pid]; ok {
			out[pid] = d
		}
	}
	return out
}

func TestOriginRecordedWhenParentExitsWhileDaemonRuns(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := clock.NewFake(t0)
	b := &cwdSpawnBackend{
		fakeSpawnBackend: fakeSpawnBackend{uidLimit: 1000, totalLimit: 10000, argv: map[int]string{100: "claude", 200: "/bin/zsh -c ./gradlew run"}},
		cwds:             map[int]string{100: "/w/lane-7", 200: "/w/lane-7/app"},
	}
	svc := NewService(ServiceOptions{Store: &countingSpawnStore{DB: testDB(t)}, SpawnBackend: b, UID: func() int { return 501 }, Clock: clk})
	t.Cleanup(svc.stopSpawnPersister)
	agent := SpawnProcess{PID: 100, PPID: 1, UID: 501, Comm: "claude", Start: t0.Add(-time.Hour)}
	shell := SpawnProcess{PID: 200, PPID: 100, UID: 501, Comm: "zsh", Start: t0.Add(-time.Minute)}
	server := SpawnProcess{PID: 300, PPID: 200, UID: 501, Comm: "java", Start: t0.Add(2*time.Second + 400*time.Millisecond)}
	tick := func(rows ...SpawnProcess) {
		t.Helper()
		b.set(rows)
		if err := svc.TickSpawn(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.Advance(5 * time.Second)
	}
	tick(agent, shell)
	tick(agent, shell, server)
	server.PPID = 1
	tick(agent, server) // the shell exited; the server is an orphan now

	params, _ := json.Marshal(map[string]any{"processes": []spawnorigin.Query{
		{PID: 300, StartTime: t0.Add(2 * time.Second)}, // ps lstart resolution
		{PID: 300, StartTime: t0.Add(time.Hour)},       // a later owner of pid 300
	}})
	out, err := svc.originsRPC(context.Background(), &daemon.Request{Params: params})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(spawnorigin.Response)
	a := resp.Answers[0]
	if !a.Found || a.Record.PPID != 200 || len(a.Ancestors) != 2 {
		t.Fatalf("answer = %+v", a)
	}
	parent := a.Ancestors[0]
	if parent.Argv != "/bin/zsh -c ./gradlew run" || parent.Cwd != "/w/lane-7/app" || !parent.ExitedAt.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("parent = %+v", parent)
	}
	if g := a.Ancestors[1]; g.Comm != "claude" || g.Cwd != "/w/lane-7" || !g.ExitedAt.IsZero() {
		t.Fatalf("grandparent = %+v", g)
	}
	if resp.Answers[1].Found {
		t.Fatalf("pid reuse matched: %+v", resp.Answers[1])
	}
	if !resp.RecordingSince.Equal(t0) {
		t.Fatalf("recording since %v", resp.RecordingSince)
	}
}

func TestLinuxStart(t *testing.T) {
	btime := time.Unix(1_700_000_000, 0)
	// "S ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt utime stime cutime cstime priority nice threads itrealvalue starttime"
	fields := strings.Fields("S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345")
	if got := linuxStart(fields, btime); !got.Equal(btime.Add(123450 * time.Millisecond)) {
		t.Fatalf("start = %v", got)
	}
	if !linuxStart(fields[:10], btime).IsZero() || !linuxStart(fields, time.Time{}).IsZero() {
		t.Fatal("unknown start not zero")
	}
}

func TestOriginsRPCRejectsTooMany(t *testing.T) {
	svc := NewService(ServiceOptions{SpawnBackend: &fakeSpawnBackend{}, UID: func() int { return 501 }})
	params, _ := json.Marshal(map[string]any{"processes": make([]spawnorigin.Query, spawnorigin.MaxQueries+1)})
	if _, err := svc.originsRPC(context.Background(), &daemon.Request{Params: params}); err == nil {
		t.Fatal("oversized request accepted")
	}
}

func TestOriginStoreBoundedInService(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	clk := clock.NewFake(t0)
	b := &fakeSpawnBackend{uidLimit: 1000, totalLimit: 10000, argv: map[int]string{}}
	svc := NewService(ServiceOptions{Store: &countingSpawnStore{DB: testDB(t)}, SpawnBackend: b, UID: func() int { return 501 }, Clock: clk, OriginMaxRecords: 8})
	t.Cleanup(svc.stopSpawnPersister)
	for i := range 10 {
		rows := make([]SpawnProcess, 5)
		for j := range rows {
			rows[j] = SpawnProcess{PID: 1000 + i*5 + j, PPID: 1, UID: 501, Comm: "x", Start: clk.Now()}
		}
		b.set(rows)
		if err := svc.TickSpawn(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.Advance(5 * time.Second)
	}
	if n := svc.origins.Len(); n > 8 {
		t.Fatalf("origin store holds %d records, bound 8", n)
	}
}
