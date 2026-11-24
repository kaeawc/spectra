package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemonhost"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/idle"
)

type fakeDaemon struct {
	ok      bool
	err     error
	reply   any
	method  string
	params  any
	timeout time.Duration
	calls   int
}

func (f *fakeDaemon) call(_ context.Context, method string, params, out any, timeout time.Duration) (bool, error) {
	f.calls++
	f.method, f.params, f.timeout = method, params, timeout
	if !f.ok || f.err != nil {
		return f.ok, f.err
	}
	data, _ := json.Marshal(f.reply)
	return true, json.Unmarshal(data, out)
}

func TestRunLoadAnswersFromDaemon(t *testing.T) {
	d := &fakeDaemon{ok: true, reply: hostload.Report{Verdict: "backoff", ExitCode: 4, CPUBasis: "window", CPUWindowMS: 812}}
	var out, errb bytes.Buffer
	code := runLoadVia([]string{"--json", "--root", "rel/wt"}, &out, &errb, fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n"), d.call)
	if code != 4 {
		t.Fatalf("exit = %d, want the daemon report's 4; stderr %s", code, errb.String())
	}
	var rep hostload.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || rep.CPUWindowMS != 812 {
		t.Fatalf("report = %+v (%v), want the daemon's", rep, err)
	}
	p := d.params.(daemonhost.LoadParams)
	if d.method != daemonhost.MethodLoad || len(p.Roots) != 1 || !filepath.IsAbs(p.Roots[0]) || !strings.HasSuffix(p.Roots[0], "rel/wt") {
		t.Fatalf("sent %s %+v, want load.report with an absolute root", d.method, p)
	}
	if p.CallerPID != os.Getpid() {
		t.Fatalf("caller_pid = %d, want this process", p.CallerPID)
	}
	if p.SampleMS != 500 || d.timeout <= 500*time.Millisecond {
		t.Fatalf("sample_ms=%d timeout=%v", p.SampleMS, d.timeout)
	}
}

func TestRunLoadFallsBackToLocal(t *testing.T) {
	deps := fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n")
	tests := []struct {
		name    string
		d       *fakeDaemon
		args    []string
		calls   int
		wantErr string
	}{
		{"no daemon", &fakeDaemon{}, []string{"--json"}, 1, ""},
		{"daemon error", &fakeDaemon{err: errors.New("collection timed out")}, []string{"--json"}, 1, "daemon: collection timed out; measuring locally"},
		{"--no-daemon", &fakeDaemon{ok: true}, []string{"--json", "--no-daemon"}, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := runLoadVia(tc.args, &out, &errb, deps, tc.d.call)
			if code != 0 || tc.d.calls != tc.calls {
				t.Fatalf("exit=%d calls=%d, want 0 and %d; stderr %s", code, tc.d.calls, tc.calls, errb.String())
			}
			var rep hostload.Report
			if err := json.Unmarshal(out.Bytes(), &rep); err != nil || rep.Verdict != "admit" || rep.CPUs != 4 {
				t.Fatalf("report = %+v (%v), want the local admit", rep, err)
			}
			if !strings.Contains(errb.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want %q", errb.String(), tc.wantErr)
			}
		})
	}
}

func TestRunIdleAnswersFromDaemon(t *testing.T) {
	root := t.TempDir()
	d := &fakeDaemon{ok: true, reply: idle.Report{Roots: []string{root}, WindowMS: 5600, Result: idle.Result{Holders: []idle.Holder{{PID: 20}}}}}
	deps, window := fakeIdleDeps(nil)
	var out, errb bytes.Buffer
	code := runIdleVia([]string{"--json", "--root", root, "--window", "2s"}, &out, &errb, deps, d.call)
	if code != idleExitFound {
		t.Fatalf("exit = %d, want %d; stderr %s", code, idleExitFound, errb.String())
	}
	if *window != 0 {
		t.Fatal("sampled locally despite a daemon answer")
	}
	p := d.params.(daemonhost.IdleParams)
	if d.method != daemonhost.MethodIdle || p.WindowMS != 2000 || p.ExcludePID <= 0 || p.Roots[0] != root {
		t.Fatalf("sent %s %+v", d.method, p)
	}
}

func TestRunIdleBadRootStaysLocal(t *testing.T) {
	d := &fakeDaemon{ok: true}
	deps, _ := fakeIdleDeps(nil)
	var out, errb bytes.Buffer
	code := runIdleVia([]string{"--root", filepath.Join(t.TempDir(), "missing")}, &out, &errb, deps, d.call)
	if code != idleExitError || d.calls != 0 {
		t.Fatalf("exit=%d calls=%d, want a local error and no daemon call", code, d.calls)
	}
}
