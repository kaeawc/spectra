package timeline

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMergeSortsByTime(t *testing.T) {
	a := []Event{{Time: at("2026-01-01T10:00:03Z"), Source: "log", Summary: "c"}}
	b := []Event{
		{Time: at("2026-01-01T10:00:01Z"), Source: "process", Summary: "a"},
		{Time: at("2026-01-01T10:00:02Z"), Source: "update", Summary: "b"},
	}
	tl := Merge(a, b)
	got := []string{tl.Events[0].Summary, tl.Events[1].Summary, tl.Events[2].Summary}
	if strings.Join(got, "") != "abc" {
		t.Errorf("order = %v, want a,b,c", got)
	}
}

func TestMergeTieBreak(t *testing.T) {
	ts := at("2026-01-01T10:00:00Z")
	tl := Merge([]Event{
		{Time: ts, Source: "process", Summary: "z"},
		{Time: ts, Source: "log", Summary: "a"},
		{Time: ts, Source: "process", Summary: "y"},
	})
	// Same time → by source then summary: log/a, process/y, process/z.
	if tl.Events[0].Source != "log" || tl.Events[1].Summary != "y" || tl.Events[2].Summary != "z" {
		t.Errorf("tie-break wrong: %+v", tl.Events)
	}
}

func TestSinceFilters(t *testing.T) {
	tl := Merge([]Event{
		{Time: at("2026-01-01T09:00:00Z"), Summary: "old"},
		{Time: at("2026-01-01T10:00:00Z"), Summary: "edge"},
		{Time: at("2026-01-01T11:00:00Z"), Summary: "new"},
	})
	got := tl.Since(at("2026-01-01T10:00:00Z"))
	if len(got.Events) != 2 || got.Events[0].Summary != "edge" {
		t.Errorf("since filter = %+v", got.Events)
	}
}

type fakeSource struct {
	name string
	evs  []Event
	err  error
}

func (f fakeSource) Name() string                      { return f.name }
func (f fakeSource) Events(time.Time) ([]Event, error) { return f.evs, f.err }

func TestCollectBestEffort(t *testing.T) {
	since := at("2026-01-01T00:00:00Z")
	good := fakeSource{name: "good", evs: []Event{{Time: at("2026-01-01T10:00:00Z"), Source: "good", Summary: "ok"}}}
	bad := fakeSource{name: "bad", err: errors.New("boom")}
	tl, errs := Collect(since, good, bad)
	if len(tl.Events) != 1 || tl.Events[0].Summary != "ok" {
		t.Errorf("a failing source must not drop the others: %+v", tl.Events)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "bad:") {
		t.Errorf("expected a per-source error, got %v", errs)
	}
}

func TestCollectAppliesWindow(t *testing.T) {
	since := at("2026-01-01T10:00:00Z")
	src := fakeSource{name: "s", evs: []Event{
		{Time: at("2026-01-01T09:00:00Z"), Summary: "old"},
		{Time: at("2026-01-01T11:00:00Z"), Summary: "new"},
	}}
	tl, _ := Collect(since, src)
	if len(tl.Events) != 1 || tl.Events[0].Summary != "new" {
		t.Errorf("Collect should drop pre-window events: %+v", tl.Events)
	}
}

func TestRenderEmptyAndPopulated(t *testing.T) {
	var empty bytes.Buffer
	if err := (Timeline{}).Render(&empty); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(empty.String(), "no events") {
		t.Errorf("empty render = %q", empty.String())
	}
	var buf bytes.Buffer
	_ = Merge([]Event{{Time: at("2026-01-01T10:00:00Z"), Source: "process", Severity: SeverityWarn, Summary: "hello"}}).Render(&buf)
	if !strings.Contains(buf.String(), "warn") || !strings.Contains(buf.String(), "hello") {
		t.Errorf("render = %q", buf.String())
	}
}

func TestRenderSanitizesControlBytes(t *testing.T) {
	var buf bytes.Buffer
	_ = Merge([]Event{{Time: at("2026-01-01T10:00:00Z"), Source: "pro\x1bcess", Summary: "ev\x07il\x1b[31m"}}).Render(&buf)
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("control bytes not stripped: %q", out)
	}
}

func TestRenderPropagatesWriteError(t *testing.T) {
	tl := Merge([]Event{{Time: at("2026-01-01T10:00:00Z"), Source: "s", Summary: "x"}})
	if err := tl.Render(failWriter{}); err == nil {
		t.Error("expected a write error to propagate")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
