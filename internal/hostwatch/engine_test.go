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
