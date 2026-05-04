package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestNewWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Writer: &buf, Format: FormatJSON, Level: slog.LevelInfo})

	l.Info("hello", "k", 42)

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, buf.String())
	}
	if rec["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", rec["msg"])
	}
	if rec["k"] != float64(42) {
		t.Errorf("k = %v, want 42", rec["k"])
	}
}

func TestNewTextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Writer: &buf, Format: FormatText})
	l.Info("started", "addr", ":8080")
	if !strings.Contains(buf.String(), "msg=started") {
		t.Errorf("text output missing msg=started: %q", buf.String())
	}
	if !strings.Contains(buf.String(), `addr=:8080`) {
		t.Errorf("text output missing addr: %q", buf.String())
	}
}

func TestLevelGating(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Writer: &buf, Format: FormatJSON, Level: slog.LevelWarn})
	l.Debug("dbg")
	l.Info("inf")
	l.Warn("wrn")
	if got := strings.Count(buf.String(), "\n"); got != 1 {
		t.Errorf("expected 1 record at >=Warn, got %d:\n%s", got, buf.String())
	}
}

func TestWithPrependsArgs(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Writer: &buf, Format: FormatJSON})
	child := l.With("svc", "auth")
	child.Info("ok")
	if !strings.Contains(buf.String(), `"svc":"auth"`) {
		t.Errorf("missing svc=auth: %s", buf.String())
	}
}

func TestCaptureBasics(t *testing.T) {
	c := NewCapture(slog.LevelDebug)
	c.Info("hello", "k", 1)
	c.Warn("careful", "n", 2)

	recs := c.Records()
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0].Level != slog.LevelInfo || recs[0].Msg != "hello" || recs[0].Attrs["k"] != 1 {
		t.Errorf("rec0 = %+v", recs[0])
	}
	if !c.HasMessage("careful") {
		t.Error("HasMessage(careful) = false")
	}
	if !c.HasMessage("car") {
		t.Error("HasMessage(substr) = false")
	}
}

func TestCaptureLevelGate(t *testing.T) {
	c := NewCapture(slog.LevelWarn)
	c.Debug("dbg")
	c.Info("inf")
	c.Warn("wrn")
	c.Error("err")
	if got := len(c.Records()); got != 2 {
		t.Errorf("got %d records, want 2", got)
	}
}

func TestCaptureFilterLevel(t *testing.T) {
	c := NewCapture(slog.LevelDebug)
	c.Info("a")
	c.Warn("b")
	c.Info("c")

	infos := c.FilterLevel(slog.LevelInfo)
	if len(infos) != 2 {
		t.Errorf("got %d info records, want 2", len(infos))
	}
}

func TestCaptureReset(t *testing.T) {
	c := NewCapture(slog.LevelDebug)
	c.Info("x")
	c.Reset()
	if got := len(c.Records()); got != 0 {
		t.Errorf("after Reset got %d records, want 0", got)
	}
}

func TestCaptureConcurrent(t *testing.T) {
	c := NewCapture(slog.LevelDebug)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Info("msg", "i", i)
		}(i)
	}
	wg.Wait()
	if got := len(c.Records()); got != 100 {
		t.Errorf("got %d records, want 100", got)
	}
}

func TestCaptureEnabled(t *testing.T) {
	c := NewCapture(slog.LevelInfo)
	if c.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("Enabled(Debug) should be false")
	}
	if !c.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(Info) should be true")
	}
}

func TestDiscardSwallowsEverything(t *testing.T) {
	l := Discard()
	l.Info("noop")
	l.Error("also noop")
	// No assertion — the point is no panic and no allocation cost matters.
}

func TestFromSlogPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	l := FromSlog(base)
	l.Info("via-from-slog")
	if !strings.Contains(buf.String(), "via-from-slog") {
		t.Errorf("FromSlog did not write through: %s", buf.String())
	}
}

func TestLoggerEnabledOnSlogImpl(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{Writer: &buf, Format: FormatJSON, Level: slog.LevelWarn})
	if l.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(Info) at Warn threshold should be false")
	}
	if !l.Enabled(context.Background(), slog.LevelError) {
		t.Error("Enabled(Error) at Warn threshold should be true")
	}
}
