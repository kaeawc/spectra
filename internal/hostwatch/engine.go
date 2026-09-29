package hostwatch

import (
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/idgen"
)

type Alert struct {
	ID         string      `json:"id"`
	Key        string      `json:"key"`
	Severity   string      `json:"severity"`
	Title      string      `json:"title"`
	Detail     string      `json:"detail"`
	State      string      `json:"state"`
	FiredAt    time.Time   `json:"fired_at"`
	ResolvedAt *time.Time  `json:"resolved_at,omitempty"`
	AckedAt    *time.Time  `json:"acked_at,omitempty"`
	Spawn      *SpawnState `json:"spawn,omitempty"`
}
type Event struct {
	Type  string `json:"type"`
	Alert Alert  `json:"alert"`
}
type Engine struct {
	active  map[string]Alert
	clears  map[string]int
	parents map[string]int
	IDs     idgen.Generator
}

func NewEngine(ids idgen.Generator) *Engine {
	if ids == nil {
		ids = idgen.UUID{}
	}
	return &Engine{active: map[string]Alert{}, clears: map[string]int{}, parents: map[string]int{}, IDs: ids}
}
func (e *Engine) Evaluate(at time.Time, conditions []Condition) []Event {
	out := append(e.EvaluateSlow(at, conditions), e.evaluate(at, conditions, "spawn", 3, -1)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Alert.Key < out[j].Alert.Key })
	return out
}

func (e *Engine) EvaluateSlow(at time.Time, conditions []Condition) []Event {
	return e.evaluate(at, conditions, "slow", 2, 0)
}

func (e *Engine) EvaluateSpawn(at time.Time, conditions []Condition, topPID ...int) []Event {
	pid := -1
	if len(topPID) > 0 {
		pid = topPID[0]
	}
	return e.evaluate(at, conditions, "spawn", 3, pid)
}

func spawnKey(key string) bool {
	return key == "spawn.rate" || key == "procs.uid" || key == "procs.total"
}

func withinScope(scope, key string) bool {
	if scope == "spawn" {
		return spawnKey(key)
	}
	return scope != "slow" || !spawnKey(key)
}

func (e *Engine) evaluate(at time.Time, conditions []Condition, scope string, clearAfter int, topPID int) []Event {
	seen := map[string]bool{}
	var out []Event
	for _, c := range conditions {
		if !withinScope(scope, c.Key) {
			continue
		}
		seen[c.Key] = true
		if event := e.applyCondition(at, c, scope, topPID); event != nil {
			out = append(out, *event)
		}
	}
	out = append(out, e.resolveMissing(at, seen, scope, clearAfter)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Alert.Key < out[j].Alert.Key })
	return out
}

func (e *Engine) applyCondition(at time.Time, c Condition, scope string, topPID int) *Event {
	a, ok := e.active[c.Key]
	e.clears[c.Key] = 0
	if !ok {
		a = Alert{ID: e.IDs.Next(), Key: c.Key, Severity: c.Severity, Title: c.Title, Detail: c.Detail, State: "firing", FiredAt: at}
		e.active[c.Key] = a
		if scope == "spawn" && topPID >= 0 {
			e.parents[c.Key] = topPID
		}
		return &Event{"fired", a}
	}
	severityChanged := a.Severity != c.Severity
	priorPID, knownParent := e.parents[c.Key]
	parentChanged := scope == "spawn" && topPID >= 0 && knownParent && priorPID != topPID
	if a.Title != c.Title || a.Detail != c.Detail || severityChanged {
		a.Severity, a.Title, a.Detail = c.Severity, c.Title, c.Detail
		e.active[c.Key] = a
	}
	if scope == "spawn" && topPID >= 0 {
		e.parents[c.Key] = topPID
	}
	if severityChanged || parentChanged {
		return &Event{"updated", a}
	}
	return nil
}

func (e *Engine) resolveMissing(at time.Time, seen map[string]bool, scope string, clearAfter int) []Event {
	var out []Event
	for key, a := range e.active {
		if !withinScope(scope, key) {
			continue
		}
		if seen[key] {
			continue
		}
		e.clears[key]++
		if e.clears[key] < clearAfter {
			continue
		}
		t := at
		a.State = "resolved"
		a.ResolvedAt = &t
		out = append(out, Event{"resolved", a})
		delete(e.active, key)
		delete(e.clears, key)
		delete(e.parents, key)
	}
	return out
}
func (e *Engine) Active() []Alert {
	out := make([]Alert, 0, len(e.active))
	for _, a := range e.active {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func (e *Engine) Restore(alerts []Alert) {
	for _, a := range alerts {
		if a.State == "firing" {
			e.active[a.Key] = a
		}
	}
}
func (e *Engine) Ack(id string, at time.Time) (Alert, bool) {
	for key, a := range e.active {
		if a.ID == id {
			a.AckedAt = &at
			e.active[key] = a
			return a, true
		}
	}
	return Alert{}, false
}
