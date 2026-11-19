package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// Ctrl-C before the first macOS interval completes prints an empty,
// clearly marked summary and exits 0.
func TestRunNetworkTopInterruptedBeforeFirstInterval(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		deps, _ := topFakeDeps(hostos.Darwin, topRuns...)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		args := []string{"--duration", "2s"}
		if asJSON {
			args = append(args, "--json")
		}
		var out, errb bytes.Buffer
		if code := runNetworkTopWithIO(ctx, args, &out, &errb, deps); code != 0 {
			t.Fatalf("json=%v code %d: %s", asJSON, code, errb.String())
		}
		s := out.String()
		if !strings.Contains(s, "interrupted before the first interval completed") {
			t.Errorf("json=%v missing interruption note:\n%s", asJSON, s)
		}
		if asJSON && (!strings.Contains(s, `"duration_ms": 0`) || !strings.Contains(s, `"remotes_basis": "end-snapshot"`)) {
			t.Errorf("bad json:\n%s", s)
		}
	}
}

// A run whose every sample failed still exits 1.
func TestRunNetworkTopAllSamplesFailedExits1(t *testing.T) {
	deps, f := topFakeDeps(hostos.Darwin, topRuns...)
	f.err = errors.New("boom")
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s"}, &out, &errb, deps); code != 1 {
		t.Fatalf("code %d, want 1: %s", code, errb.String())
	}
}

func TestRunNetworkTopRemotesBasis(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topRuns...)
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	for _, want := range []string{"REMOTES (open at end of window)", "connections open at the end of the window"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("macOS missing %q:\n%s", want, out.String())
		}
	}
	ss := "ESTAB 0 0 10.0.0.5:51000 140.82.112.3:443 users:((\"git\",pid=200,fd=5))\n\t cubic bytes_acked:1 bytes_received:10\n"
	deps, _ = topFakeDeps(hostos.Linux, ss, ss)
	out.Reset()
	if code := runNetworkTopWithIO(context.Background(), []string{"--json", "--duration", "1s"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"remotes_basis": "window-bytes"`) {
		t.Errorf("linux basis missing:\n%s", out.String())
	}
}

func TestTruncateByRune(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 4, "abc…"},
		{"abcd", 4, "abcd"},
		{"héllo wörld", 5, "héll…"},
		{"日本語のテキスト", 4, "日本語…"},
		{"日本語", 3, "日本語"},
	}
	for _, c := range cases {
		got := truncate(c.in, c.n)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("truncate(%q,%d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// Linux rejects intervals below 100ms as a usage error before sampling.
func TestRunNetworkTopLinuxMinInterval(t *testing.T) {
	deps, f := topFakeDeps(hostos.Linux, "")
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--interval", "1ns", "--duration", "10s"}, &out, &errb, deps); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "at least 100ms") || f.calls != 0 {
		t.Errorf("stderr = %q calls = %d", errb.String(), f.calls)
	}
	deps, _ = topFakeDeps(hostos.Linux, "")
	if code := runNetworkTopWithIO(context.Background(), []string{"--interval", "100ms", "--duration", "300ms"}, &out, &errb, deps); code != 0 {
		t.Errorf("100ms: code = %d: %s", code, errb.String())
	}
}

type procRunnerFunc func(ctx context.Context, cmd proc.Cmd) (proc.Result, error)

func (f procRunnerFunc) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	return f(ctx, cmd)
}

// A Linux baseline followed only by failed polls measured nothing: exit 1
// naming ss, not the idle message.
func TestRunNetworkTopBaselineOnlyExits1(t *testing.T) {
	deps, f := topFakeDeps(hostos.Linux, "")
	runner := deps.runner
	deps.runner = procRunnerFunc(func(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
		if f.calls > 0 {
			f.calls++
			return proc.Result{ExitCode: 1, Stderr: []byte("netlink busy")}, nil
		}
		return runner.Run(ctx, cmd)
	})
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s"}, &out, &errb, deps); code != 1 {
		t.Fatalf("code = %d, want 1:\n%s", code, out.String())
	}
	if !strings.Contains(errb.String(), "from ss") || !strings.Contains(errb.String(), "no interval was measured") || strings.Contains(out.String(), "no process moved") {
		t.Errorf("stderr = %q out = %q", errb.String(), out.String())
	}
}

// started/stopped come from measured activity and are labelled as such in
// text and JSON.
func TestRunNetworkTopStartedStopped(t *testing.T) {
	runs := []string{
		nettopRun("Google Chrome H.100,1000,0,", "Slack.200,50,0,"),
		nettopRun("Google Chrome H.100,1000,0,", "curl.101,10,0,"),
	}
	deps, _ := topFakeDeps(hostos.Darwin, runs...)
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "started using the network after the first interval: curl (101)") ||
		!strings.Contains(s, "stopped using the network before the last interval: Slack (200)") {
		t.Errorf("text:\n%s", s)
	}
	deps, _ = topFakeDeps(hostos.Darwin, runs...)
	out.Reset()
	if code := runNetworkTopWithIO(context.Background(), []string{"--json", "--duration", "2s"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	for _, want := range []string{`"started": [`, `"stopped": [`, `"started": true`, `"stopped": true`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("json missing %s:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "appeared") {
		t.Errorf("old field name in json:\n%s", out.String())
	}
}
