package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/process"
)

func lineageFakeProcs() process.Collector {
	return process.CollectorFunc(func(context.Context, process.CollectOptions) []process.Info {
		return []process.Info{
			{PID: 1, PPID: 0, Command: "launchd", UID: 0, User: "root"},
			{PID: 10, PPID: 1, Command: "zsh", UID: 501, User: "me", RSSKiB: 100},
			{PID: 20, PPID: 10, Command: "make", UID: 501, User: "me", RSSKiB: 200, CPUPct: 5},
		}
	})
}

func TestRunLineage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runLineageWithIO([]string{"10"}, &out, &errb, lineageFakeProcs()); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{"Ancestors", "launchd", "zsh", "make", "reparented", "2 processes"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRunLineageJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runLineageWithIO([]string{"--json", "20"}, &out, &errb, lineageFakeProcs()); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["pid"].(float64) != 20 {
		t.Errorf("pid = %v", m["pid"])
	}
}

func TestRunLineageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
	}{
		{"no args", nil, 2},
		{"bad pid", []string{"abc"}, 2},
		{"zero pid", []string{"0"}, 2},
		{"bad flag", []string{"--nope", "1"}, 2},
		{"unknown pid", []string{"999"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := runLineageWithIO(tt.args, &out, &errb, lineageFakeProcs()); code != tt.code {
				t.Errorf("code = %d, want %d (%s)", code, tt.code, errb.String())
			}
		})
	}
}
