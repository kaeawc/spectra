package logquery

import (
	"context"
	"errors"
	"time"
)

var (
	ErrWindowTooLarge  = errors.New("log query window too large")
	ErrUnsafePredicate = errors.New("raw predicate requires unsafe opt-in")
)

type Query struct {
	Predicate            string
	Process              string
	Subsystem            string
	MinLevel             string
	Start                time.Time
	End                  time.Time
	Last                 time.Duration
	MaxRows              int
	MaxWindow            time.Duration
	AllowLongWindow      bool
	AllowUnsafePredicate bool
	Runner               Runner
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout []byte, stderr []byte, err error)
}

type RunnerFunc func(context.Context, string, ...string) ([]byte, []byte, error)

func (f RunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	return f(ctx, name, args...)
}

type LogEntry struct {
	Timestamp        time.Time `json:"timestamp"`
	Process          string    `json:"process,omitempty"`
	PID              int32     `json:"pid,omitempty"`
	Sender           string    `json:"sender,omitempty"`
	SenderImagePath  string    `json:"sender_image_path,omitempty"`
	Subsystem        string    `json:"subsystem,omitempty"`
	Category         string    `json:"category,omitempty"`
	EventType        string    `json:"event_type,omitempty"`
	LogType          string    `json:"log_type,omitempty"`
	MessageType      string    `json:"message_type,omitempty"`
	ThreadID         uint64    `json:"thread_id,omitempty"`
	EventMessage     string    `json:"event_message,omitempty"`
	FormatString     string    `json:"format_string,omitempty"`
	ActivityID       uint64    `json:"activity_id,omitempty"`
	ParentActivityID uint64    `json:"parent_activity_id,omitempty"`
	TraceID          uint64    `json:"trace_id,omitempty"`
}

type Result struct {
	Entries   []LogEntry `json:"entries"`
	Truncated bool       `json:"truncated,omitempty"`
	Clock     struct {
		WallClockAdjusted bool `json:"wall_clock_adjusted,omitempty"`
	} `json:"clock"`
	QueryDuration time.Duration `json:"query_duration"`
}
