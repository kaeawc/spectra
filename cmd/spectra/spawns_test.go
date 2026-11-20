package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

func TestRunSpawnsTextAndJSON(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mk := func(withChild bool) []process.Info {
		ps := []process.Info{
			{PID: 1, Command: "launchd", StartTime: t0.Add(-time.Hour)},
			{PID: 100, PPID: 1, Command: "zsh", StartTime: t0.Add(-time.Minute)},
		}
		if withChild {
			ps = append(ps, process.Info{PID: 200, PPID: 100, Command: "make", StartTime: t0})
		}
		return ps
	}
	newDeps := func() spawnsDeps {
		clk := clock.NewFake(t0)
		ticks := make(chan time.Time, 10)
		for i := 0; i < 10; i++ {
			ticks <- t0
		}
		tables := [][]process.Info{mk(false), mk(true), mk(false)}
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

	var out, errBuf bytes.Buffer
	if code := runSpawnsWithIO(context.Background(), []string{"--interval", "1s", "--duration", "3s"}, &out, &errBuf, newDeps()); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"START", "EXIT", "make", "zsh < launchd", "Summary: 1 started, 1 exited", "polling misses"} {
		if !strings.Contains(s, want) {
			t.Errorf("text output missing %q:\n%s", want, s)
		}
	}

	out.Reset()
	if code := runSpawnsWithIO(context.Background(), []string{"--interval", "1s", "--duration", "3s", "--json"}, &out, &errBuf, newDeps()); code != 0 {
		t.Fatalf("json exit = %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("json lines = %d, want 2 events + summary:\n%s", len(lines), out.String())
	}
	for i, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
	}
	var last map[string]any
	_ = json.Unmarshal([]byte(lines[2]), &last)
	if last["type"] != "summary" || last["starts"].(float64) != 1 {
		t.Errorf("summary line = %v", last)
	}
}

func TestRunSpawnsUnknownPID(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	deps := spawnsDeps{
		Collect: func(context.Context) []process.Info {
			return []process.Info{{PID: 1, Command: "launchd", StartTime: t0}}
		},
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
		Clock:  clock.NewFake(t0),
	}
	var out, errBuf bytes.Buffer
	code := runSpawnsWithIO(context.Background(), []string{"--pid", "4242", "--duration", "1h"}, &out, &errBuf, deps)
	if code != 1 || !strings.Contains(errBuf.String(), "4242 not found") {
		t.Fatalf("exit = %d stderr=%q", code, errBuf.String())
	}
}

func TestRunSpawnsArgValidation(t *testing.T) {
	for _, args := range [][]string{{"--interval", "0"}, {"--duration", "-1s"}, {"--pid", "-2"}, {"extra"}, {"--interval", "5s", "--duration", "1s"}} {
		var out, errBuf bytes.Buffer
		if code := runSpawnsWithIO(context.Background(), args, &out, &errBuf, spawnsDeps{}); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}
