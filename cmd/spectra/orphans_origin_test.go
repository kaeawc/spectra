package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/spawnorigin"
)

type orphanOriginRow struct {
	PID    int `json:"pid"`
	Origin struct {
		Known  bool   `json:"known"`
		Reason string `json:"reason"`
		Parent *struct {
			PID         int        `json:"pid"`
			CommandLine string     `json:"command_line"`
			Cwd         string     `json:"cwd"`
			ExitedAt    *time.Time `json:"exited_at"`
		} `json:"parent"`
		Agent *struct {
			PID  int    `json:"pid"`
			Role string `json:"role"`
		} `json:"agent"`
	} `json:"origin"`
}

func decodeOrphanRows(t *testing.T, b []byte) map[int]orphanOriginRow {
	t.Helper()
	var rep struct {
		Groups []struct {
			Processes []orphanOriginRow `json:"processes"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	out := map[int]orphanOriginRow{}
	for _, g := range rep.Groups {
		for _, p := range g.Processes {
			out[p.PID] = p
		}
	}
	return out
}

func TestRunOrphansReportsRecordedOrigin(t *testing.T) {
	deps := orphansFakeDeps()
	old := orphansNow.Add(-time.Hour)
	exited := orphansNow.Add(-30 * time.Minute)
	var asked []spawnorigin.Query
	deps.Origins = func(_ context.Context, q []spawnorigin.Query) orphans.OriginSource {
		asked = q
		return orphans.OriginSource{Response: spawnorigin.Response{Answers: []spawnorigin.Answer{{
			PID: 10, StartTime: old, Found: true,
			Record: &spawnorigin.Record{PID: 10, StartTime: old, PPID: 70, ParentStart: old.Add(-time.Minute), ParentComm: "zsh"},
			Chain: []spawnorigin.Record{
				{PID: 70, StartTime: old.Add(-time.Minute), PPID: 60, ParentStart: old.Add(-time.Hour), Comm: "zsh", Argv: "zsh -c npm run dev", Cwd: "/private/tmp/work/wt1", ExitedAt: exited},
				{PID: 60, StartTime: old.Add(-time.Hour), PPID: 1, Comm: "claude", Argv: "claude"},
			},
		}}}}
	}
	deps.Roles = func(in []classify.Input) []classify.Role {
		out := make([]classify.Role, len(in))
		for i, x := range in {
			if x.Command == "claude" {
				out[i] = classify.RoleAIAgent
			}
		}
		return out
	}
	var out, errb bytes.Buffer
	if code := runOrphansWithIO([]string{"--json"}, &out, &errb, deps); code != 3 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if len(asked) != 2 {
		t.Fatalf("queried %+v", asked)
	}
	rows := decodeOrphanRows(t, out.Bytes())
	o := rows[10].Origin
	if !o.Known || o.Parent == nil || o.Parent.PID != 70 || o.Parent.CommandLine != "zsh -c npm run dev" || o.Parent.ExitedAt == nil || !o.Parent.ExitedAt.Equal(exited) {
		t.Fatalf("origin = %+v", o)
	}
	if o.Agent == nil || o.Agent.PID != 60 || o.Agent.Role != "ai_agent" {
		t.Fatalf("agent = %+v", o.Agent)
	}
	if r := rows[11].Origin; r.Known || r.Reason != orphans.ReasonNoRecord {
		t.Fatalf("unrecorded origin = %+v", r)
	}
	out.Reset()
	runOrphansWithIO(nil, &out, &errb, deps)
	if !strings.Contains(out.String(), "origin: zsh -c npm run dev (pid 70, exited by") || !strings.Contains(out.String(), "agent: claude (pid 60, ai_agent)") {
		t.Fatalf("text output:\n%s", out.String())
	}
}

func TestRunOrphansOriginUnknownWithoutDaemon(t *testing.T) {
	deps := orphansFakeDeps() // Origins nil: no daemon
	var out, errb bytes.Buffer
	runOrphansWithIO([]string{"--json"}, &out, &errb, deps)
	for pid, r := range decodeOrphanRows(t, out.Bytes()) {
		if r.Origin.Known || r.Origin.Reason != originNoDaemon {
			t.Fatalf("pid %d origin = %+v", pid, r.Origin)
		}
	}
	deps.Origins = func(context.Context, []spawnorigin.Query) orphans.OriginSource {
		t.Fatal("daemon asked despite --no-daemon")
		return orphans.OriginSource{}
	}
	out.Reset()
	runOrphansWithIO([]string{"--json", "--no-daemon"}, &out, &errb, deps)
	for _, r := range decodeOrphanRows(t, out.Bytes()) {
		if r.Origin.Known || !strings.Contains(r.Origin.Reason, "--no-daemon") {
			t.Fatalf("origin = %+v", r.Origin)
		}
	}
}

func TestOriginLookupError(t *testing.T) {
	if s := originLookupError(&daemonclient.RPCError{Code: daemon.CodeMethodNotFound}); !strings.Contains(s, "restart") {
		t.Fatalf("method-not-found reason = %q", s)
	}
	if s := originLookupError(errors.New("boom")); !strings.Contains(s, "boom") {
		t.Fatalf("reason = %q", s)
	}
}
