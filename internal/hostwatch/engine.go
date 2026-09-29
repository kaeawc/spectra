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
	active map[string]Alert
	clears map[string]int
	IDs    idgen.Generator
}

func NewEngine(ids idgen.Generator) *Engine {
	if ids == nil {
		ids = idgen.UUID{}
	}
	return &Engine{active: map[string]Alert{}, clears: map[string]int{}, IDs: ids}
}
func (e *Engine) Evaluate(at time.Time, conditions []Condition) []Event {
	return e.evaluate(at, conditions, "", 2)
}

func (e *Engine) EvaluateSlow(at time.Time, conditions []Condition) []Event {
	return e.evaluate(at, conditions, "slow", 2)
}

func (e *Engine) EvaluateSpawn(at time.Time, conditions []Condition) []Event {
	return e.evaluate(at, conditions, "spawn", 3)
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

func (e *Engine) evaluate(at time.Time, conditions []Condition, scope string, clearAfter int) []Event {
	seen := map[string]bool{}
	var out []Event
	for _, c := range conditions {
		if !withinScope(scope, c.Key) {
			continue
		}
		seen[c.Key] = true
		a, ok := e.active[c.Key]
		e.clears[c.Key] = 0
		if !ok {
			a = Alert{ID: e.IDs.Next(), Key: c.Key, Severity: c.Severity, Title: c.Title, Detail: c.Detail, State: "firing", FiredAt: at}
			e.active[c.Key] = a
			out = append(out, Event{"fired", a})
			continue
		}
		if a.Severity != c.Severity {
			a.Severity = c.Severity
			a.Title = c.Title
			a.Detail = c.Detail
			e.active[c.Key] = a
			out = append(out, Event{"updated", a})
		}
	}
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
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alert.Key < out[j].Alert.Key })
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
