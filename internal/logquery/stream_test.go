package logquery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStreamReaderEmitsEntries(t *testing.T) {
	entries, errs := StreamReader(context.Background(), strings.NewReader("Filtering the log data using process == kernel\n"+ndjsonFixture))
	var got []LogEntry
	for entry := range entries {
		got = append(got, entry)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	if got[0].EventMessage != "fs_snapshot_list failed" {
		t.Fatalf("first entry = %+v", got[0])
	}
	if err := <-errs; err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}
}

func TestStreamReaderReportsDecodeError(t *testing.T) {
	_, errs := StreamReader(context.Background(), strings.NewReader("{bad json}\n"))
	if err := <-errs; err == nil {
		t.Fatal("expected decode error")
	}
}

func TestBuildStreamArgs(t *testing.T) {
	args := buildStreamArgs(Query{Process: "kernel", MinLevel: "Error"})
	got := strings.Join(args, " ")
	if !strings.Contains(got, "stream") || !strings.Contains(got, "process == \"kernel\"") || !strings.Contains(got, "messageType == \"Error\"") {
		t.Fatalf("args = %v", args)
	}
}

func TestStreamRejectsUnsafePredicate(t *testing.T) {
	_, _, err := Stream(context.Background(), Query{Predicate: "eventMessage CONTAINS 'x'"})
	if !errors.Is(err, ErrUnsafePredicate) {
		t.Fatalf("err = %v, want ErrUnsafePredicate", err)
	}
}
