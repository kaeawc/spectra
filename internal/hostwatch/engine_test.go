package hostwatch

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/idgen"
)

func TestEngineEpisodes(t *testing.T) {
	e := NewEngine(idgen.NewSequence("a"))
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	warn := Condition{"load.high", "warn", "High", "detail"}
	critical := Condition{"load.high", "critical", "Critical", "detail"}
	check := func(cs []Condition, want string) {
		t.Helper()
		ev := e.Evaluate(at, cs)
		at = at.Add(time.Second)
		if want == "" && len(ev) == 0 {
			return
		}
		if len(ev) != 1 || ev[0].Type != want {
			t.Fatalf("events=%+v want %s", ev, want)
		}
	}
	check([]Condition{warn}, "fired")
	check([]Condition{warn}, "")
	check([]Condition{critical}, "updated")
	check(nil, "")
	check(nil, "resolved")
	check([]Condition{warn}, "fired")
}

func TestEngineSpawnParentRotationAndDetailRefresh(t *testing.T) {
	e := NewEngine(idgen.NewSequence("s"))
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := Condition{"procs.uid", "warn", "Processes", "pid 10"}
	if got := e.Evaluate(at, []Condition{c}); len(got) != 1 || got[0].Type != "fired" {
		t.Fatalf("initial: %+v", got)
	}
	c.Detail = "pid 10, new count"
	if got := e.EvaluateSpawn(at.Add(time.Second), []Condition{c}, 10); len(got) != 0 {
		t.Fatalf("first known top parent: %+v", got)
	}
	c.Detail = "pid 10, another count"
	if got := e.EvaluateSpawn(at.Add(2*time.Second), []Condition{c}, 10); len(got) != 0 {
		t.Fatalf("detail-only event: %+v", got)
	}
	if e.Active()[0].Detail != c.Detail {
		t.Fatal("active detail did not refresh")
	}
	c.Detail = "pid 20"
	if got := e.EvaluateSpawn(at.Add(3*time.Second), []Condition{c}, 20); len(got) != 1 || got[0].Type != "updated" || got[0].Alert.Detail != c.Detail {
		t.Fatalf("rotation: %+v", got)
	}
	for i := 0; i < 2; i++ {
		if got := e.Evaluate(at.Add(time.Duration(4+i)*time.Second), nil); len(got) != 0 {
			t.Fatalf("cleared too early: %+v", got)
		}
	}
	if got := e.Evaluate(at.Add(6*time.Second), nil); len(got) != 1 || got[0].Type != "resolved" {
		t.Fatalf("spawn clear threshold: %+v", got)
	}
}
