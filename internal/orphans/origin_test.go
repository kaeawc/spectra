package orphans

import (
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spawnorigin"
)

// nameRoles is a fake classifier keyed by command name.
func nameRoles(in []classify.Input) []classify.Role {
	out := make([]classify.Role, len(in))
	for i, x := range in {
		switch x.Command {
		case "claude":
			out[i] = classify.RoleAIAgent
		case "zsh":
			out[i] = classify.RoleShell
		default:
			out[i] = classify.RoleCLI
		}
	}
	return out
}

func originFixture() ([]process.Info, Result) {
	procs := []process.Info{
		proc(10, 1, 100, time.Hour),            // orphan with a recorded origin
		proc(11, 1, 100, 2*time.Hour),          // orphan, no record
		{PID: 12, PPID: 1, Command: "nostart"}, // no start time
		proc(100, 1, 1, 3*time.Hour),           // the agent, still alive
	}
	res := Result{Groups: []Group{{Processes: []Process{{PID: 10}, {PID: 11}, {PID: 12}}}}}
	return procs, res
}

func TestAttachOriginsKnown(t *testing.T) {
	procs, res := originFixture()
	exited := now.Add(-50 * time.Minute)
	src := OriginSource{Response: spawnorigin.Response{
		RecordingSince: now.Add(-4 * time.Hour),
		Answers: []spawnorigin.Answer{
			{PID: 10, StartTime: now.Add(-time.Hour), Found: true,
				Record: &spawnorigin.Record{PID: 10, StartTime: now.Add(-time.Hour).Add(400 * time.Millisecond), PPID: 200, ParentStart: now.Add(-2 * time.Hour), ParentComm: "zsh", FirstSeen: now.Add(-time.Hour)},
				Chain: []spawnorigin.Record{
					{PID: 200, StartTime: now.Add(-2 * time.Hour), PPID: 100, ParentStart: now.Add(-3 * time.Hour), Comm: "zsh", Argv: "zsh -c npm run dev", Cwd: "/r/lane-7", ExitedAt: exited},
					{PID: 100, StartTime: now.Add(-3 * time.Hour), PPID: 1, Comm: "claude", Argv: "claude", Cwd: "/r"},
				}},
			{PID: 11, StartTime: now.Add(-2 * time.Hour)},
		},
	}}
	if q := Queries(res, procs); len(q) != 2 || q[0].PID != 10 || q[1].PID != 11 {
		t.Fatalf("queries = %+v", q)
	}
	AttachOrigins(&res, procs, src, nameRoles)
	ps := res.Groups[0].Processes
	o := ps[0].Origin
	if o == nil || !o.Known || o.Parent == nil || o.Parent.PID != 200 || o.Parent.Alive || o.Parent.ExitedAt == nil || !o.Parent.ExitedAt.Equal(exited) || o.Parent.Cwd != "/r/lane-7" {
		t.Fatalf("origin = %+v parent=%+v", o, o.Parent)
	}
	if o.Session == nil || o.Session.PID != 200 || o.Session.Role != "shell" {
		t.Fatalf("session = %+v", o.Session)
	}
	if o.Agent == nil || o.Agent.PID != 100 || !o.Agent.Alive || o.Agent.Depth != 2 {
		t.Fatalf("agent = %+v", o.Agent)
	}
	if ps[1].Origin.Known || ps[1].Origin.Reason != ReasonNoRecord {
		t.Fatalf("no-record origin = %+v", ps[1].Origin)
	}
	if ps[2].Origin.Known || ps[2].Origin.Reason != ReasonNoStartTime {
		t.Fatalf("no-start origin = %+v", ps[2].Origin)
	}
}

func TestAttachOriginsPIDReuse(t *testing.T) {
	procs, res := originFixture()
	// The daemon answers for an earlier owner of pid 10; the answer's start
	// time does not match the live process, so it must not attach.
	src := OriginSource{Response: spawnorigin.Response{Answers: []spawnorigin.Answer{
		{PID: 10, StartTime: now.Add(-5 * time.Hour), Found: true, Record: &spawnorigin.Record{PID: 10, PPID: 200, ParentStart: now, ParentComm: "zsh"}},
	}}}
	AttachOrigins(&res, procs, src, nameRoles)
	if o := res.Groups[0].Processes[0].Origin; o.Known || o.Reason != ReasonNoRecord {
		t.Fatalf("reused pid got origin %+v", o)
	}
}

func TestAttachOriginsNoParentAndUnrecordedParent(t *testing.T) {
	procs, res := originFixture()
	start := now.Add(-time.Hour)
	src := OriginSource{Response: spawnorigin.Response{Answers: []spawnorigin.Answer{
		{PID: 10, StartTime: start, Found: true, Record: &spawnorigin.Record{PID: 10, PPID: 1}},
		{PID: 11, StartTime: now.Add(-2 * time.Hour), Found: true, Record: &spawnorigin.Record{PID: 11, PPID: 5, ParentStart: now.Add(-9 * time.Hour), ParentComm: "sshd"}},
	}}}
	AttachOrigins(&res, procs, src, nameRoles)
	if o := res.Groups[0].Processes[0].Origin; o.Known || o.Reason != ReasonNoParent {
		t.Fatalf("orphan-at-first-sight origin = %+v", o)
	}
	o := res.Groups[0].Processes[1].Origin
	if !o.Known || o.Parent.Command != "sshd" || o.Parent.Alive || o.Session != nil {
		t.Fatalf("unrecorded parent origin = %+v %+v", o, o.Parent)
	}
}

func TestAttachOriginsUnavailable(t *testing.T) {
	procs, res := originFixture()
	AttachOrigins(&res, procs, OriginSource{Unavailable: "spectra daemon is not running"}, nil)
	for _, p := range res.Groups[0].Processes {
		if p.Origin == nil || p.Origin.Known || !strings.Contains(p.Origin.Reason, "not running") {
			t.Fatalf("pid %d origin = %+v", p.PID, p.Origin)
		}
	}
}
