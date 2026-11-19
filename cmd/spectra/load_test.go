package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
)

func fakeLoadDeps(load, meminfo string) loadDeps {
	files := map[string]string{"/proc/loadavg": load, "/proc/meminfo": meminfo}
	return loadDeps{
		Host: hostload.Host{
			OS: hostos.Linux,
			ReadFile: func(p string) ([]byte, error) {
				if s, ok := files[p]; ok {
					return []byte(s), nil
				}
				return nil, errors.New("missing")
			},
			Readlink: func(p string) (string, error) {
				if p == "/proc/10/cwd" {
					return "/r/wt1/sub", nil
				}
				return "", errors.New("denied")
			},
			NumCPU: func() int { return 4 },
		},
		Procs: func(context.Context) []process.Info {
			return []process.Info{
				{PID: 10, Command: "go", CPUPct: 90, RSSKiB: 1024},
				{PID: 11, Command: "mystery", CPUPct: 5, RSSKiB: 2048},
			}
		},
		FS:  hostload.FS{Exists: func(p string) bool { return p == "/r/wt1/.git" }},
		Now: time.Now,
	}
}

func TestRunLoadExitCodesAndJSON(t *testing.T) {
	tests := []struct {
		name, load, mem string
		args            []string
		want            int
		verdict         string
	}{
		{"admit", "1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n", nil, 0, "admit"},
		{"hold on load", "6.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n", nil, 3, "hold"},
		{"backoff", "1.00 1 1 1/1 1", "MemAvailable: 2097152 kB\n", nil, 4, "backoff"},
		{"unknown memory fails closed", "1.00 1 1 1/1 1", "", nil, 3, "hold"},
		{"flag moves limit", "6.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n", []string{"--max-load-per-cpu", "2"}, 0, "admit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			args := append([]string{"--json", "--root", "/r"}, tc.args...)
			code := runLoadWithIO(args, &out, &errb, fakeLoadDeps(tc.load, tc.mem))
			if code != tc.want {
				t.Fatalf("exit = %d, want %d; stderr=%s", code, tc.want, errb.String())
			}
			var m map[string]any
			if err := json.Unmarshal(out.Bytes(), &m); err != nil {
				t.Fatal(err)
			}
			if m["verdict"] != tc.verdict {
				t.Fatalf("verdict = %v", m["verdict"])
			}
			for _, k := range []string{"reason", "exit_code", "load1", "load_limit", "cpus", "available_gb", "cause", "top_cpu", "top_rss", "families", "dirs", "unknown", "elapsed_ms"} {
				if _, ok := m[k]; !ok {
					t.Fatalf("missing json field %q in %s", k, out.String())
				}
			}
			cause := m["cause"].(map[string]any)
			if cause["kind"] != "roots" || cause["worktree"] != "wt1" {
				t.Fatalf("cause = %v", cause)
			}
			if tc.name == "unknown memory fails closed" && m["available_gb"] != nil {
				t.Fatalf("available_gb should be null, got %v", m["available_gb"])
			}
		})
	}
}

func TestRunLoadTextAndUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runLoadWithIO(nil, &out, &errb, fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n")); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"verdict  ADMIT", "top-cpu", "unknown (cwd unreadable)", "elapsed"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Fatalf("text output lacks %q:\n%s", want, out.String())
		}
	}
	if code := runLoadWithIO([]string{"--top", "0"}, &out, &errb, fakeLoadDeps("1 1 1 1/1 1", "")); code != 2 {
		t.Fatalf("usage exit = %d", code)
	}
	if code := runLoadWithIO([]string{"--bogus"}, &out, &errb, fakeLoadDeps("1 1 1 1/1 1", "")); code != 2 {
		t.Fatalf("bad flag exit = %d", code)
	}
	if code := runLoadWithIO(nil, &out, &errb, fakeLoadDeps("garbage", "")); code != 1 {
		t.Fatalf("error exit = %d", code)
	}
}

func TestRunLoadRelativeRoot(t *testing.T) {
	deps := fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n")
	deps.Abs = func(p string) (string, error) { return "/" + p, nil }
	var out, errb bytes.Buffer
	if code := runLoadWithIO([]string{"--json", "--root", "r"}, &out, &errb, deps); code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, errb.String())
	}
	var m struct {
		Cause struct{ Kind, Root, Worktree string }
	}
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Cause.Kind != "roots" || m.Cause.Root != "r" || m.Cause.Worktree != "wt1" {
		t.Fatalf("cause = %+v", m.Cause)
	}
}
