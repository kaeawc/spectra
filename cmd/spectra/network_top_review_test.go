package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kaeawc/spectra/internal/hostos"
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
	if !strings.Contains(out.String(), `"remotes_basis": "busiest-sockets"`) {
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
