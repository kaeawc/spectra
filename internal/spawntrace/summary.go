package spawntrace

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

const (
	// LoopThreshold is how many starts of the same command under the same
	// parent count as a respawn loop.
	LoopThreshold = 3
	shortestKept  = 5
	// TopKept is how many by-parent and by-command entries are listed.
	TopKept = 10
)

// Count is a named tally.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Lived is an exited process and how long it lasted.
type Lived struct {
	PID      int           `json:"pid"`
	Command  string        `json:"command"`
	Lifetime time.Duration `json:"-"`
}

// Loop is a command that was started repeatedly by one parent.
type Loop struct {
	Parent  string `json:"parent"`
	Command string `json:"command"`
	Count   int    `json:"count"`
}

// Summary aggregates a trace. Use Add to feed events and Result for the
// sorted final form.
type Summary struct {
	Starts            int           `json:"starts"`
	Exits             int           `json:"exits"`
	Polls             int           `json:"polls"`
	SkippedTicks      int           `json:"skipped_ticks"`
	Duration          time.Duration `json:"-"`
	Interval          time.Duration `json:"-"`
	EffectiveInterval time.Duration `json:"-"`
	Note              string        `json:"note"`
	ByParent          []Count       `json:"by_parent"`
	ByCommand         []Count       `json:"by_command"`
	// ByParentTotal and ByCommandTotal are the distinct counts before the
	// lists are cut to the top topKept entries.
	ByParentTotal  int     `json:"by_parent_total"`
	ByCommandTotal int     `json:"by_command_total"`
	RootExited     bool    `json:"root_exited,omitempty"`
	RootNotFound   bool    `json:"-"`
	Shortest       []Lived `json:"shortest_lived"`
	Loops          []Loop  `json:"respawn_loops"`

	parents  map[string]int
	commands map[string]int
	loops    map[loopKey]loopVal
	lived    []Lived
}

type loopVal struct {
	parent string
	n      int
}

type loopKey struct {
	ppid    int
	command string
}

// NewSummary returns an empty Summary.
func NewSummary() *Summary {
	return &Summary{
		parents:  map[string]int{},
		commands: map[string]int{},
		loops:    map[loopKey]loopVal{},
	}
}

func parentLabel(e Event) string {
	if len(e.Chain) == 0 {
		return fmt.Sprintf("pid %d", e.PPID)
	}
	return fmt.Sprintf("%s[%d]", e.Chain[0], e.PPID)
}

// Add folds one event into the summary.
func (s *Summary) Add(e Event) {
	switch e.Kind {
	case KindStart:
		s.Starts++
		s.parents[parentLabel(e)]++
		s.commands[e.Command]++
		k := loopKey{e.PPID, e.Command}
		v := s.loops[k]
		v.parent, v.n = parentLabel(e), v.n+1
		s.loops[k] = v
	case KindExit:
		s.Exits++
		s.addLived(Lived{PID: e.PID, Command: e.Command, Lifetime: e.Lifetime})
	}
}

// addLived keeps only the shortestKept shortest lifetimes, sorted ascending.
func (s *Summary) addLived(l Lived) {
	i := sort.Search(len(s.lived), func(i int) bool { return s.lived[i].Lifetime > l.Lifetime })
	if i >= shortestKept {
		return
	}
	s.lived = append(s.lived, Lived{})
	copy(s.lived[i+1:], s.lived[i:])
	s.lived[i] = l
	if len(s.lived) > shortestKept {
		s.lived = s.lived[:shortestKept]
	}
}

func ranked(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for k, v := range m {
		out = append(out, Count{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > TopKept {
		out = out[:TopKept]
	}
	return out
}

// Result returns a copy with the ranked lists filled in.
func (s *Summary) Result() Summary {
	out := *s
	out.ByParent = ranked(s.parents)
	out.ByCommand = ranked(s.commands)

	out.ByParentTotal = len(s.parents)
	out.ByCommandTotal = len(s.commands)
	out.Shortest = append([]Lived(nil), s.lived...)

	out.Loops = []Loop{}
	for k, v := range s.loops {
		if v.n >= LoopThreshold {
			out.Loops = append(out.Loops, Loop{Parent: v.parent, Command: k.command, Count: v.n})
		}
	}
	sort.Slice(out.Loops, func(i, j int) bool {
		a, b := out.Loops[i], out.Loops[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Parent != b.Parent {
			return a.Parent < b.Parent
		}
		return a.Command < b.Command
	})
	return out
}

// MarshalJSON adds the duration fields in seconds.
func (s Summary) MarshalJSON() ([]byte, error) {
	type plain Summary
	type wire struct {
		Type string `json:"type"`
		plain
		Duration  float64 `json:"duration_seconds"`
		Interval  float64 `json:"interval_seconds"`
		Effective float64 `json:"effective_interval_seconds"`
		Shortest  []struct {
			PID      int     `json:"pid"`
			Command  string  `json:"command"`
			Lifetime float64 `json:"lifetime_seconds"`
		} `json:"shortest_lived"`
	}
	w := wire{Type: "summary", plain: plain(s)}
	w.Duration, w.Interval, w.Effective = s.Duration.Seconds(), s.Interval.Seconds(), s.EffectiveInterval.Seconds()
	for _, l := range s.Shortest {
		w.Shortest = append(w.Shortest, struct {
			PID      int     `json:"pid"`
			Command  string  `json:"command"`
			Lifetime float64 `json:"lifetime_seconds"`
		}{l.PID, l.Command, l.Lifetime.Seconds()})
	}
	return json.Marshal(w)
}
