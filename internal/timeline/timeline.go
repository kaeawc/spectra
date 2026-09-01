// Package timeline merges timestamped events from independent sources — process
// starts, unified-log entries, install/update events, and more — into one
// chronological incident view. It reads only.
package timeline

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Severity ranks an event's importance.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Event is one thing that happened at a point in time.
type Event struct {
	Time     time.Time `json:"time"`
	Source   string    `json:"source"`
	Severity Severity  `json:"severity"`
	Summary  string    `json:"summary"`
}

// Source produces events at or after the given cutoff.
type Source interface {
	// Name identifies the source (e.g. "process", "log").
	Name() string
	// Events returns this source's events no earlier than since.
	Events(since time.Time) ([]Event, error)
}

// Timeline is a chronologically ordered set of events.
type Timeline struct {
	Events []Event `json:"events"`
}

// Merge combines event groups into one timeline sorted ascending by time
// (ties broken by source then summary for stable output).
func Merge(groups ...[]Event) Timeline {
	var all []Event
	for _, g := range groups {
		all = append(all, g...)
	}
	sortEvents(all)
	return Timeline{Events: all}
}

// Since returns the events at or after cutoff (the input is assumed sorted).
func (t Timeline) Since(cutoff time.Time) Timeline {
	out := make([]Event, 0, len(t.Events))
	for _, e := range t.Events {
		if !e.Time.Before(cutoff) {
			out = append(out, e)
		}
	}
	return Timeline{Events: out}
}

// Collect gathers events from all sources within the window [since, ∞), merges
// them, and returns the timeline plus any per-source errors (collection is best
// effort: one failing source does not drop the others).
func Collect(since time.Time, sources ...Source) (Timeline, []error) {
	var groups [][]Event
	var errs []error
	for _, s := range sources {
		evs, err := s.Events(since)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
			continue
		}
		groups = append(groups, evs)
	}
	return Merge(groups...).Since(since), errs
}

// Render writes the timeline as aligned text, sanitizing event fields so a
// control byte in a source's data cannot inject terminal escape sequences. It
// returns the first write error (e.g. a broken pipe).
func (t Timeline) Render(w io.Writer) error {
	if len(t.Events) == 0 {
		_, err := fmt.Fprintln(w, "(no events in the window)")
		return err
	}
	for _, e := range t.Events {
		if _, err := fmt.Fprintf(w, "%s  %-5s  %-8s  %s\n",
			e.Time.Local().Format("2006-01-02 15:04:05"),
			e.Severity, truncateField(sanitize(e.Source), 8), sanitize(e.Summary)); err != nil {
			return err
		}
	}
	return nil
}

// sanitize strips C0/C1 control bytes from text taken from a source.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		default:
			return r
		}
	}, s)
}

func sortEvents(evs []Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		if !evs[i].Time.Equal(evs[j].Time) {
			return evs[i].Time.Before(evs[j].Time)
		}
		if evs[i].Source != evs[j].Source {
			return evs[i].Source < evs[j].Source
		}
		return evs[i].Summary < evs[j].Summary
	})
}

func truncateField(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
