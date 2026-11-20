package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/process"
)

var idleNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func fakeIdleDeps(cpu map[int]float64) (idleDeps, *time.Duration) {
	var window time.Duration
	procs := []process.Info{
		{PID: 1, Command: "init"},
		{PID: 20, PPID: 1, Command: "slot", StartTime: idleNow.Add(-time.Hour)},
		{PID: 21, PPID: 20, Command: "slot", StartTime: idleNow.Add(-time.Hour)},
		{PID: 99, PPID: 1, Command: "spectra", StartTime: idleNow},
	}
	return idleDeps{
		Sample: func(_ context.Context, w time.Duration) (idle.Sample, error) {
			window = w
			return idle.Sample{Procs: procs, CPU: cpu, Window: w, Basis: idle.BasisWindow}, nil
		},
		Cwds: func(context.Context, []process.Info) (map[int]string, bool, error) {
			return map[int]string{20: "/work/lane", 21: "/work/lane/sub", 99: "/work/lane"}, true, nil
		},
		Waits: func(_ context.Context, pids []int) map[int]*idle.Wait {
			return map[int]*idle.Wait{21: {WChan: "futex_wait_queue", LockFiles: []string{"/work/lane/slot.lock"}}}
		},
		Now:          func() time.Time { return idleNow },
		Pid:          func() int { return 99 },
		Getwd:        func() (string, error) { return "/work/lane", nil },
		Abs:          func(s string) (string, error) { return s, nil },
		EvalSymlinks: func(s string) (string, error) { return s, nil },
	}, &window
}

func TestRunIdle(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		cpu      map[int]float64
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "idle tree", cpu: map[int]float64{20: 0, 21: 0.1}, wantCode: 3, wantOut: "lock file: /work/lane/slot.lock"},
		{name: "busy child", cpu: map[int]float64{20: 0, 21: 80}, wantCode: 0, wantOut: "No idle holders under /work/lane (checked 2: 2 busy"},
		{name: "by pid", args: []string{"--pids", "21,555"}, cpu: map[int]float64{21: 0}, wantCode: 3, wantOut: "Not running: 555"},
		{name: "bad pids", args: []string{"--pids", "x"}, wantCode: 2, wantErr: "invalid pid"},
		{name: "bad window", args: []string{"--window", "1ms"}, wantCode: 2, wantErr: "invalid arguments"},
		{name: "timeout below window", args: []string{"--window", "5s", "--timeout", "1s"}, wantCode: 2},
		{name: "extra arg", args: []string{"x"}, wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _ := fakeIdleDeps(tt.cpu)
			var out, errb bytes.Buffer
			if code := runIdleWithIO(tt.args, &out, &errb, deps); code != tt.wantCode {
				t.Fatalf("code = %d, want %d (out=%q err=%q)", code, tt.wantCode, out.String(), errb.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errb.String(), tt.wantErr) {
				t.Errorf("out=%q err=%q", out.String(), errb.String())
			}
		})
	}
}

func TestRunIdleJSON(t *testing.T) {
	deps, window := fakeIdleDeps(map[int]float64{20: 0, 21: 0})
	var out bytes.Buffer
	if code := runIdleWithIO([]string{"--json", "--root", "/work/lane", "--window", "2s", "--min-age", "30m"}, &out, &bytes.Buffer{}, deps); code != 3 {
		t.Fatalf("code = %d: %s", code, out.String())
	}
	if *window != 2*time.Second {
		t.Errorf("window = %v", *window)
	}
	var got struct {
		CPUBasis   string `json:"cpu_basis"`
		Incomplete bool   `json:"incomplete"`
		Result     struct {
			Holders []struct {
				PID         int `json:"pid"`
				Descendants []struct {
					PID  int        `json:"pid"`
					Wait *idle.Wait `json:"wait"`
				} `json:"descendants"`
				Chain []idle.Link `json:"chain"`
			} `json:"holders"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	hs := got.Result.Holders
	if got.CPUBasis != "window" || !got.Incomplete || len(hs) != 1 || hs[0].PID != 20 || len(hs[0].Chain) != 1 ||
		len(hs[0].Descendants) != 1 || hs[0].Descendants[0].Wait == nil || hs[0].Descendants[0].Wait.WChan != "futex_wait_queue" {
		t.Errorf("json = %s", out.String())
	}
}

func TestRunIdleSampleError(t *testing.T) {
	deps, _ := fakeIdleDeps(nil)
	deps.Sample = func(context.Context, time.Duration) (idle.Sample, error) {
		return idle.Sample{}, errors.New("sampling CPU: context deadline exceeded")
	}
	var errb bytes.Buffer
	if code := runIdleWithIO(nil, &bytes.Buffer{}, &errb, deps); code != 1 || !strings.Contains(errb.String(), "deadline") {
		t.Errorf("code = %d err = %q", code, errb.String())
	}
}
