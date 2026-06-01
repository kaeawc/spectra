package logquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const ndjsonFixture = `{"timestamp":"2026-06-01 13:27:26.997252-0500","messageType":"Info","eventType":"logEvent","processImagePath":"/usr/libexec/backupd","senderImagePath":"/usr/libexec/backupd","processID":123,"threadID":7,"eventMessage":"fs_snapshot_list failed","formatString":"%{public}s","activityIdentifier":9,"parentActivityIdentifier":8,"traceID":6}
{"timestamp":"2026-06-01 13:27:27.000000-0500","messageType":"Error","eventType":"logEvent","processImagePath":"/usr/libexec/backupd","processID":123,"eventMessage":"Snapshot deletion not completed"}
`

func TestRunParsesNDJSONAndStderrClockWarning(t *testing.T) {
	run := RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name != "/usr/bin/log" || len(args) < 4 {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return []byte(ndjsonFixture), []byte("Wall Clock adjustment detected"), nil
	})
	result, err := Run(context.Background(), Query{Process: "backupd", Last: time.Minute, MaxRows: 10, Runner: run})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	if result.Entries[0].Process != "backupd" || result.Entries[0].PID != 123 || result.Entries[0].LogType != "Info" {
		t.Fatalf("entry not parsed: %+v", result.Entries[0])
	}
	if result.Entries[0].Timestamp.IsZero() {
		t.Fatal("timestamp was not parsed")
	}
	if !result.Clock.WallClockAdjusted {
		t.Fatal("wall-clock warning not detected")
	}
}

func TestRunRejectsLargeWindow(t *testing.T) {
	_, err := Run(context.Background(), Query{Last: 30 * 24 * time.Hour})
	if !errors.Is(err, ErrWindowTooLarge) {
		t.Fatalf("err = %v, want ErrWindowTooLarge", err)
	}
}

func TestRunRejectsUnsafePredicate(t *testing.T) {
	_, err := Run(context.Background(), Query{Predicate: `eventMessage CONTAINS "x"`})
	if !errors.Is(err, ErrUnsafePredicate) {
		t.Fatalf("err = %v, want ErrUnsafePredicate", err)
	}
}

func TestRunHonorsMaxRows(t *testing.T) {
	run := RunnerFunc(func(context.Context, string, ...string) ([]byte, []byte, error) {
		return []byte(strings.Repeat(ndjsonFixture, 60)), nil, nil
	})
	result, err := Run(context.Background(), Query{Last: time.Minute, MaxRows: 100, Runner: run})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 100 || !result.Truncated {
		t.Fatalf("entries=%d truncated=%v, want 100 true", len(result.Entries), result.Truncated)
	}
}

func TestBuildPredicate(t *testing.T) {
	q := Query{Process: `backup"d`, Subsystem: "com.apple.TimeMachine", MinLevel: "Error"}
	got := buildPredicate(q)
	for _, want := range []string{`process == "backup\"d"`, `subsystem == "com.apple.TimeMachine"`, `messageType == "Error"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("predicate %q missing %q", got, want)
		}
	}
}
