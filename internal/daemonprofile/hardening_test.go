package daemonprofile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostos"
)

const spindumpReport = "Duration:         2.0s\n\nProcess:          Foo [4012]\n  100  Thread_1\n"

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func jobCount(s *Service) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

func TestDurationOverflowRejected(t *testing.T) {
	// 2^58 ms wraps to 0 when multiplied into a time.Duration.
	for _, ms := range []int64{288230376151711744, 1 << 62, 999, 60001} {
		if _, err := duration(ms, time.Second); err == nil {
			t.Errorf("duration_ms %d accepted", ms)
		}
	}
}

func TestDecodeErrorIsPreserved(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin})
	_, err := s.startRPC(context.Background(), &daemon.Request{Params: json.RawMessage(`{"kind":"sample","pid":"1"}`)})
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Fatalf("err = %v (%T), want it to wrap *json.UnmarshalTypeError", err, err)
	}
}

func TestReadInputFileEnforcesLimit(t *testing.T) {
	p := writeTemp(t, "big.txt", strings.Repeat("x", 100))
	if _, err := ReadInputFile(p, 99); err == nil || !strings.Contains(err.Error(), "over the 99-byte input limit") {
		t.Fatalf("err = %v", err)
	}
	if data, err := ReadInputFile(p, 100); err != nil || len(data) != 100 {
		t.Fatalf("at the limit: %d bytes, %v", len(data), err)
	}
}

func TestInputOverLimitFailsJob(t *testing.T) {
	p := writeTemp(t, "report.txt", spindumpReport)
	s := NewService(Options{OS: hostos.Darwin, MaxInputBytes: 10})
	v, err := call(t, s, start, StartParams{Kind: KindSpindump, Input: p})
	if err != nil {
		t.Fatal(err)
	}
	if rec := waitDone(t, s, v.(Job).ID); rec.Status != StatusFailed || !strings.Contains(rec.Error, "over the 10-byte input limit") {
		t.Fatalf("record = %+v", rec)
	}
}

func mkfifo(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	return p
}

func TestNonRegularInputRejectedBeforeStart(t *testing.T) {
	fifo := mkfifo(t)
	s := NewService(Options{OS: hostos.Darwin})
	for _, p := range []StartParams{
		{Kind: KindSpindump, Input: fifo},
		{Kind: KindFlamegraph, Input: fifo, Out: filepath.Join(t.TempDir(), "g.svg")},
		{Kind: KindSpindump, Input: t.TempDir()},
	} {
		_, err := call(t, s, start, p)
		if !strings.Contains(errString(err), "not a regular file") {
			t.Errorf("%+v: err = %v", p, err)
		}
	}
	if n := jobCount(s); n != 0 {
		t.Fatalf("%d jobs started", n)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A FIFO swapped in after validation must fail the read, not block it.
func TestReadInputFileDoesNotBlockOnFIFO(t *testing.T) {
	fifo := mkfifo(t)
	done := make(chan error, 1)
	go func() { _, err := ReadInputFile(fifo, 1<<20); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReadInputFile blocked on a FIFO with no writer")
	}
}

// The terminal status reads ctx under the lock: a deadline that passes while
// the worker waits for the lock is a timeout, not a completion.
func TestDeadlineWhileCommittingIsTimeout(t *testing.T) {
	release := make(chan struct{})
	returned := make(chan struct{})
	s := NewService(Options{OS: hostos.Darwin, Slack: 200 * time.Millisecond, ReadInput: func(string, int64) ([]byte, error) {
		<-release
		close(returned)
		return []byte(spindumpReport), nil
	}})
	v, err := call(t, s, start, StartParams{Kind: KindSpindump, Input: "/report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	close(release)
	<-returned
	time.Sleep(400 * time.Millisecond) // the deadline passes while the worker waits for mu
	s.mu.Unlock()
	if rec := waitDone(t, s, v.(Job).ID); rec.Status != StatusFailed || !strings.Contains(rec.Error, "timed out") {
		t.Fatalf("record = %+v, want a timeout", rec)
	}
}

func TestMaxFinishedEnforcedOnFinish(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin, MaxFinished: 1, SampleCapture: fixedSample})
	for i := 0; i < 2; i++ {
		if _, err := call(t, s, start, StartParams{Kind: KindSample, PID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// No further RPC: the second job to finish must evict the first.
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		active, n := s.active, len(s.jobs)
		s.mu.Unlock()
		if active == 0 {
			if n != 1 {
				t.Fatalf("%d finished jobs held, want 1", n)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("jobs did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExpiredResultsReleasedWithoutRPC(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin, ResultTTL: 50 * time.Millisecond, SweepInterval: 10 * time.Millisecond, SampleCapture: fixedSample})
	defer s.Close(time.Second)
	if _, err := call(t, s, start, StartParams{Kind: KindSample, PID: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; jobCount(s) != 0; i++ {
		if i > 500 {
			t.Fatal("expired result still held")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCloseStopsSweep(t *testing.T) {
	s := NewService(Options{SweepInterval: time.Millisecond})
	done := make(chan struct{})
	go func() { s.Close(5 * time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	select {
	case <-s.base.Done():
	default:
		t.Fatal("service context still live after Close")
	}
}

func workingJob(s *Service) (*job, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{cancel: cancel, rec: Job{ID: "j", Kind: KindFlamegraph, Status: StatusWorking}}
	s.mu.Lock()
	s.jobs[j.rec.ID] = j
	s.mu.Unlock()
	return j, ctx
}

// Cancellation that wins leaves out untouched and no staged file behind.
func TestPublishLosesToCancel(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin})
	j, ctx := workingJob(s)
	if _, err := call(t, s, cancel, IDParams{ID: j.rec.ID}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "g.svg")
	if err := s.publish(ctx, j, out, []byte("<svg/>"), 0o644); !errors.Is(err, context.Canceled) {
		t.Fatalf("publish err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left behind %v", entries)
	}
}

// A publish that wins is final: cancel is refused and the job completes.
func TestPublishWinsOverCancel(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin})
	j, ctx := workingJob(s)
	out := filepath.Join(t.TempDir(), "g.svg")
	if err := s.publish(ctx, j, out, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("published file: %v, %v", fi, err)
	}
	v, err := call(t, s, cancel, IDParams{ID: j.rec.ID})
	if err != nil || v.(Job).Status != StatusWorking {
		t.Fatalf("cancel after publish = %+v, %v; want still working", v, err)
	}
	if ctx.Err() != nil {
		t.Fatal("cancel after publish cancelled the job context")
	}
}

// End to end: a cancel issued while the capture is finishing never leaves an
// SVG at out once the job reports cancelled.
func TestCancelledFlamegraphWritesNothing(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	s := NewService(Options{OS: hostos.Darwin, FlamegraphCapture: func(context.Context, int, int) (string, error) {
		close(started)
		<-release // ignores ctx, as a capture finishing its last read might
		return "Call graph:\n    100 Thread_1\n      100 main  (in App) + 1  [0x1]\n", nil
	}})
	out := filepath.Join(t.TempDir(), "g.svg")
	v, err := call(t, s, start, StartParams{Kind: KindFlamegraph, PID: 1, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if v, err := call(t, s, cancel, IDParams{ID: v.(Job).ID}); err != nil || v.(Job).Status != StatusCancelled {
		t.Fatalf("cancel = %+v, %v", v, err)
	}
	close(release)
	s.Close(5 * time.Second)
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("out exists after cancel: %v", err)
	}
}
