package orphans

import (
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spawnorigin"
)

// Origin is what the daemon recorded about how a candidate started. Known is
// false, with Reason saying why, when there is no daemon, no record, or the
// record has no parent (the daemon first saw the process already orphaned).
type Origin struct {
	Known          bool       `json:"known"`
	Reason         string     `json:"reason,omitempty"`
	RecordingSince *time.Time `json:"recording_since,omitempty"`
	FirstSeen      *time.Time `json:"first_seen,omitempty"`
	// Parent is the parent the process had when the daemon first saw it.
	Parent *Ancestor `json:"parent,omitempty"`
	// Session is the nearest ancestor that is an AI agent, terminal or
	// shell; Agent is the nearest AI agent, which may be further up.
	Session *Ancestor `json:"session,omitempty"`
	Agent   *Ancestor `json:"agent,omitempty"`
}

// Ancestor is one recorded ancestor. Depth 1 is the parent. ExitedAt is the
// first daemon probe that no longer saw it (the exit was at most one probe
// interval earlier); it is unset while alive or when the daemon has not
// noticed the exit yet. Role is classified from the recorded name and argv
// only, so it is argv-based evidence, not a verified executable.
type Ancestor struct {
	PID         int        `json:"pid"`
	StartTime   time.Time  `json:"start_time,omitzero"`
	Command     string     `json:"command"`
	CommandLine string     `json:"command_line,omitempty"`
	Cwd         string     `json:"cwd,omitempty"`
	Role        string     `json:"role,omitempty"`
	Alive       bool       `json:"alive"`
	ExitedAt    *time.Time `json:"exited_at,omitempty"`
	Depth       int        `json:"depth"`
}

// OriginSource is the daemon's answer, or why there is none.
type OriginSource struct {
	Response spawnorigin.Response
	// Unavailable, when set, is why the daemon could not be asked.
	Unavailable string
}

// RoleFunc classifies processes, one role per input in order.
type RoleFunc func([]classify.Input) []classify.Role

// Reasons an origin is unknown.
const (
	ReasonNoStartTime = "start time unknown, so no record can be matched safely"
	ReasonNoRecord    = "not recorded by the daemon (started before recording began, or exited and was evicted)"
	ReasonNoParent    = "the daemon first saw this process already without its original parent"
)

func startOf(p process.Info) time.Time {
	if !p.StartTime.IsZero() {
		return p.StartTime
	}
	return p.StartedAt
}

// Queries returns the daemon lookup keys for every candidate in res.
func Queries(res Result, procs []process.Info) []spawnorigin.Query {
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	var out []spawnorigin.Query
	for _, g := range res.Groups {
		for _, row := range g.Processes {
			if start := startOf(byPID[row.PID]); !start.IsZero() {
				out = append(out, spawnorigin.Query{PID: row.PID, StartTime: start})
			}
		}
	}
	return out
}

// AttachOrigins sets Origin on every candidate in res. procs is the current
// process table, used to tell live ancestors from dead ones.
func AttachOrigins(res *Result, procs []process.Info, src OriginSource, roles RoleFunc) {
	live := make(map[int]time.Time, len(procs))
	for _, p := range procs {
		live[p.PID] = startOf(p)
	}
	answers := map[spawnorigin.Key]spawnorigin.Answer{}
	for _, a := range src.Response.Answers {
		answers[spawnorigin.KeyOf(a.PID, a.StartTime)] = a
	}
	var chains [][]Ancestor
	var targets []*Origin
	for gi := range res.Groups {
		for pi := range res.Groups[gi].Processes {
			row := &res.Groups[gi].Processes[pi]
			o, chain := originOf(row.PID, live, answers, src)
			row.Origin = o
			if o.Known {
				targets, chains = append(targets, o), append(chains, chain)
			}
		}
	}
	classifyChains(chains, roles)
	for i, o := range targets {
		fillSessions(o, chains[i])
	}
}

func originOf(pid int, live map[int]time.Time, answers map[spawnorigin.Key]spawnorigin.Answer, src OriginSource) (*Origin, []Ancestor) {
	if src.Unavailable != "" {
		return &Origin{Reason: src.Unavailable}, nil
	}
	o := &Origin{}
	if since := src.Response.RecordingSince; !since.IsZero() {
		o.RecordingSince = &since
	}
	start := live[pid]
	if start.IsZero() {
		o.Reason = ReasonNoStartTime
		return o, nil
	}
	a, ok := answers[spawnorigin.KeyOf(pid, start)]
	if !ok || !a.Found || a.Record == nil {
		o.Reason = ReasonNoRecord
		return o, nil
	}
	first := a.Record.FirstSeen
	o.FirstSeen = &first
	chain := chainOf(*a.Record, a.Ancestors, live)
	if len(chain) == 0 {
		o.Reason = ReasonNoParent
		return o, nil
	}
	o.Known = true
	o.Parent = &chain[0]
	return o, chain
}

// chainOf turns the recorded ancestors into Ancestors, nearest first. When
// the chain ends at a parent the daemon did not record (another user's
// process), that parent is appended from its name alone.
func chainOf(rec spawnorigin.Record, recorded []spawnorigin.Record, live map[int]time.Time) []Ancestor {
	var out []Ancestor
	cur := rec
	for _, r := range recorded {
		if r.PID != cur.PPID {
			break
		}
		out = append(out, ancestorOf(r, len(out)+1, live))
		cur = r
	}
	if cur.PPID > 1 && !cur.ParentStart.IsZero() {
		out = append(out, ancestorOf(spawnorigin.Record{PID: cur.PPID, StartTime: cur.ParentStart, Comm: cur.ParentComm}, len(out)+1, live))
	}
	return out
}

func ancestorOf(r spawnorigin.Record, depth int, live map[int]time.Time) Ancestor {
	a := Ancestor{PID: r.PID, StartTime: r.StartTime, Command: r.Comm, CommandLine: r.Argv, Cwd: r.Cwd, Depth: depth}
	if s, ok := live[r.PID]; ok && !s.IsZero() && sameStart(s, r.StartTime) {
		a.Alive = true
	} else if !r.ExitedAt.IsZero() {
		at := r.ExitedAt
		a.ExitedAt = &at
	}
	return a
}

func sameStart(a, b time.Time) bool {
	d := a.Sub(b)
	return d <= spawnorigin.StartTolerance && d >= -spawnorigin.StartTolerance
}

func classifyChains(chains [][]Ancestor, roles RoleFunc) {
	if roles == nil {
		return
	}
	var inputs []classify.Input
	for _, c := range chains {
		for _, a := range c {
			inputs = append(inputs, classify.Input{PID: a.PID, Command: a.Command, CommandLine: a.CommandLine})
		}
	}
	if len(inputs) == 0 {
		return
	}
	out := roles(inputs)
	i := 0
	for _, c := range chains {
		for j := range c {
			if i < len(out) {
				c[j].Role = string(out[i])
			}
			i++
		}
	}
}

func fillSessions(o *Origin, chain []Ancestor) {
	if len(chain) > 0 {
		o.Parent = &chain[0]
	}
	for i := range chain {
		a := &chain[i]
		switch classify.Role(a.Role) {
		case classify.RoleAIAgent:
			if o.Agent == nil {
				o.Agent = a
			}
			if o.Session == nil {
				o.Session = a
			}
		case classify.RoleTerminal, classify.RoleShell:
			if o.Session == nil {
				o.Session = a
			}
		}
	}
}
