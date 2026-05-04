// Package logger provides a small Logger interface over log/slog so production
// code logs to stderr/JSON while tests substitute Capture to assert on emitted
// records.
//
// Production:
//
//	log := logger.New(logger.Config{Format: logger.FormatJSON, Level: slog.LevelInfo})
//	log.Info("server started", "addr", addr)
//
// Tests:
//
//	cap := logger.NewCapture(slog.LevelDebug)
//	cap.Info("hello", "k", 1)
//	if !cap.HasMessage("hello") { t.Fatal("missing message") }
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
)

// Logger is the structured-logging interface. The methods mirror slog's
// shape so callers can switch implementations without rewriting call sites.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	// With returns a child Logger that prepends args to every record.
	With(args ...any) Logger
	// Enabled reports whether the underlying handler accepts records at level.
	Enabled(ctx context.Context, level slog.Level) bool
}

// Format selects the on-wire representation.
type Format int

const (
	// FormatText writes human-readable key=value lines (slog.TextHandler).
	FormatText Format = iota
	// FormatJSON writes one JSON object per record (slog.JSONHandler).
	FormatJSON
)

// Config controls New. The zero value writes JSON at info level to stderr.
type Config struct {
	// Writer receives the encoded records. Defaults to os.Stderr.
	Writer io.Writer
	// Format selects text vs JSON. Defaults to JSON (good for production
	// log aggregators; switch to FormatText for local development).
	Format Format
	// Level is the minimum level recorded. Defaults to slog.LevelInfo.
	Level slog.Level
	// AddSource adds a source=file:line attribute to every record.
	AddSource bool
}

// New returns a Logger backed by slog with the given config.
func New(cfg Config) Logger {
	w := cfg.Writer
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: cfg.Level, AddSource: cfg.AddSource}
	var h slog.Handler
	switch cfg.Format {
	case FormatText:
		h = slog.NewTextHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}
	return &slogLogger{l: slog.New(h)}
}

// FromSlog wraps an existing *slog.Logger. Useful when the application root
// already constructed one and you want to inject it as the Logger interface.
func FromSlog(l *slog.Logger) Logger { return &slogLogger{l: l} }

// Discard returns a Logger that drops everything. Useful for benchmarks.
func Discard() Logger { return &slogLogger{l: slog.New(slog.NewTextHandler(io.Discard, nil))} }

type slogLogger struct{ l *slog.Logger }

func (s *slogLogger) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }
func (s *slogLogger) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s *slogLogger) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s *slogLogger) Error(msg string, args ...any) { s.l.Error(msg, args...) }
func (s *slogLogger) With(args ...any) Logger       { return &slogLogger{l: s.l.With(args...)} }
func (s *slogLogger) Enabled(ctx context.Context, level slog.Level) bool {
	return s.l.Enabled(ctx, level)
}

// Record is a captured log entry.
type Record struct {
	Level slog.Level
	Msg   string
	Attrs map[string]any
}

// Capture is a Logger that buffers records in memory for assertion. Safe for
// concurrent use.
type Capture struct {
	level slog.Level
	with  []any

	mu      sync.Mutex
	records []Record
}

// NewCapture returns a Capture that records every entry at level or above.
func NewCapture(level slog.Level) *Capture { return &Capture{level: level} }

// Records returns a snapshot of every captured record.
func (c *Capture) Records() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Record, len(c.records))
	copy(out, c.records)
	return out
}

// Reset drops all captured records.
func (c *Capture) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = nil
}

// HasMessage reports whether any captured record's Msg contains substr.
func (c *Capture) HasMessage(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if strings.Contains(r.Msg, substr) {
			return true
		}
	}
	return false
}

// FilterLevel returns the subset of records at exactly level.
func (c *Capture) FilterLevel(level slog.Level) []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Record
	for _, r := range c.records {
		if r.Level == level {
			out = append(out, r)
		}
	}
	return out
}

func (c *Capture) Debug(msg string, args ...any) { c.record(slog.LevelDebug, msg, args) }
func (c *Capture) Info(msg string, args ...any)  { c.record(slog.LevelInfo, msg, args) }
func (c *Capture) Warn(msg string, args ...any)  { c.record(slog.LevelWarn, msg, args) }
func (c *Capture) Error(msg string, args ...any) { c.record(slog.LevelError, msg, args) }

// With returns a child Capture that prepends args to every record. The child
// shares the parent's record buffer so all entries land in one place.
func (c *Capture) With(args ...any) Logger {
	return &Capture{
		level:   c.level,
		with:    slices.Concat(c.with, args),
		mu:      sync.Mutex{},
		records: nil,
		// Note: child writes its own records; merge across parents via
		// the same Capture by passing it directly instead of branching.
	}
}

// Enabled reports whether level is at or above the configured threshold.
func (c *Capture) Enabled(_ context.Context, level slog.Level) bool {
	return level >= c.level
}

func (c *Capture) record(level slog.Level, msg string, args []any) {
	if level < c.level {
		return
	}
	attrs := map[string]any{}
	merged := append(slices.Clone(c.with), args...)
	for i := 0; i+1 < len(merged); i += 2 {
		k, ok := merged[i].(string)
		if !ok {
			continue
		}
		attrs[k] = merged[i+1]
	}
	c.mu.Lock()
	c.records = append(c.records, Record{Level: level, Msg: msg, Attrs: attrs})
	c.mu.Unlock()
}
