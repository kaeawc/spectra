package logquery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestRunAndStreamUnsupportedOffDarwin(t *testing.T) {
	called := false
	run := RunnerFunc(func(context.Context, string, ...string) ([]byte, []byte, error) {
		called = true
		return nil, nil, nil
	})
	if _, err := Run(context.Background(), Query{OS: hostos.Linux, Runner: run}); !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("Run err = %v, want ErrUnsupported", err)
	}
	if called {
		t.Fatal("Run must not invoke /usr/bin/log off Darwin")
	}
	if _, _, err := Stream(context.Background(), Query{OS: hostos.Linux}); !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("Stream err = %v, want ErrUnsupported", err)
	}
}

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
	_, _, err := Stream(context.Background(), Query{OS: hostos.Darwin, Predicate: "eventMessage CONTAINS 'x'"})
	if !errors.Is(err, ErrUnsafePredicate) {
		t.Fatalf("err = %v, want ErrUnsafePredicate", err)
	}
}
