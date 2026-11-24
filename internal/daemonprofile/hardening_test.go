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

	"github.com/kaeawc/spectra/internal/binaryimages"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/flamegraph"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/nativeprofile"
)

const spindumpReport = "Duration:         2.0s\n\nProcess:          Foo [4012]\n  100  Thread_1\n"

const fgReport = "Call graph:\n    100 Thread_1\n      100 main  (in App) + 1  [0x1]\n"

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

// waitSignal waits, bounded, for ch to close.
func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
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
	s := newTestService(t, Options{OS: hostos.Darwin})
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
	s := newTestService(t, Options{OS: hostos.Darwin, MaxInputBytes: 10})
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

// The input is only touched by the bounded worker: profile.start never
// stats it, and a non-regular input fails the job instead of blocking it.
func TestNonRegularInputFailsJob(t *testing.T) {
	fifo := mkfifo(t)
	s := newTestService(t, Options{OS: hostos.Darwin})
	for _, p := range []StartParams{
		{Kind: KindSpindump, Input: fifo},
		{Kind: KindFlamegraph, Input: fifo, Out: filepath.Join(t.TempDir(), "g.svg")},
		{Kind: KindSpindump, Input: t.TempDir()},
	} {
		v, err := call(t, s, start, p)
		if err != nil {
			t.Fatalf("%+v: start err = %v", p, err)
		}
		if rec := waitDone(t, s, v.(Job).ID); rec.Status != StatusFailed || !strings.Contains(rec.Error, "not a regular file") {
			t.Errorf("%+v: record = %+v", p, rec)
		}
	}
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
// the worker waits for the lock is a timeout, not a completion. The test
// holds mu from the moment the worker is about to take it.
func TestDeadlineWhileCommittingIsTimeout(t *testing.T) {
	s := newTestService(t, Options{OS: hostos.Darwin, Slack: 200 * time.Millisecond, ReadInput: func(string, int64) ([]byte, error) {
		return []byte(spindumpReport), nil
	}})
	atCommit := make(chan struct{})
	proceed := make(chan struct{})
	s.beforeCommit = func() { close(atCommit); <-proceed }
	v, err := call(t, s, start, StartParams{Kind: KindSpindump, Input: "/report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	waitSignal(t, atCommit, "the worker to reach the commit")
	s.mu.Lock()
	close(proceed)
	time.Sleep(400 * time.Millisecond) // the deadline passes while the worker waits for mu
	s.mu.Unlock()
	if rec := waitDone(t, s, v.(Job).ID); rec.Status != StatusFailed || !strings.Contains(rec.Error, "timed out") {
		t.Fatalf("record = %+v, want a timeout", rec)
	}
}

func TestMaxFinishedEnforcedOnFinish(t *testing.T) {
	s := newTestService(t, Options{OS: hostos.Darwin, MaxFinished: 1, SampleCapture: fixedSample})
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
	s := newTestService(t, Options{OS: hostos.Darwin, ResultTTL: 50 * time.Millisecond, SweepInterval: 10 * time.Millisecond, SampleCapture: fixedSample})
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

// With no jobs, the only goroutine Close joins is the sweep: it must exit.
func TestCloseStopsSweep(t *testing.T) {
	s := NewService(Options{SweepInterval: time.Millisecond})
	s.Close(time.Second)
	exited := make(chan struct{})
	go func() { s.wg.Wait(); close(exited) }()
	waitSignal(t, exited, "the sweep goroutine to exit")
}

// flamegraphService is a service whose flamegraph captures return fgReport
// once release closes (immediately when release is nil).
func flamegraphService(t *testing.T, opts Options, release <-chan struct{}, started chan<- struct{}) *Service {
	t.Helper()
	opts.OS = hostos.Darwin
	opts.FlamegraphCapture = func(context.Context, int, int) (string, error) {
		if started != nil {
			close(started)
		}
		if release != nil {
			<-release // ignores ctx, as a capture finishing its last read might
		}
		return fgReport, nil
	}
	return newTestService(t, opts)
}

func runFlamegraph(t *testing.T, s *Service, out string) Job {
	t.Helper()
	v, err := call(t, s, start, StartParams{Kind: KindFlamegraph, PID: 1, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	return waitDone(t, s, v.(Job).ID)
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A new out gets CreatePerm (the umask-derived CLI mode); a replaced out
// keeps its own, possibly private, mode. No staging file is left behind.
func TestPublishedSVGModes(t *testing.T) {
	dir := t.TempDir()
	s := flamegraphService(t, Options{CreatePerm: 0o640}, nil, nil)
	fresh := filepath.Join(dir, "new.svg")
	if rec := runFlamegraph(t, s, fresh); rec.Status != StatusCompleted {
		t.Fatalf("record = %+v", rec)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("new out: %v, %v; want 0640", fi, err)
	}
	private := writeTemp(t, "private.svg", "old")
	if err := os.Chmod(private, 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := runFlamegraph(t, s, private); rec.Status != StatusCompleted {
		t.Fatalf("record = %+v", rec)
	}
	data, _ := os.ReadFile(private)
	if fi, err := os.Stat(private); err != nil || fi.Mode().Perm() != 0o600 || !strings.HasPrefix(string(data), "<svg") {
		t.Fatalf("replaced out: %v, %v; want the new SVG at 0600", fi, err)
	}
	if names := dirEntries(t, dir); len(names) != 1 || names[0] != "new.svg" {
		t.Fatalf("dir holds %v", names)
	}
}

func TestDefaultCreatePermHonoursUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	if got := umaskCreatePerm(); got != 0o600 {
		t.Fatalf("umask 077: %o, want 600", got)
	}
	syscall.Umask(0o022)
	if got := umaskCreatePerm(); got != 0o644 {
		t.Fatalf("umask 022: %o, want 644", got)
	}
}

// The staging file is private and short-named, whatever out is called.
func TestStagingFileIsPrivateAndShort(t *testing.T) {
	dir := t.TempDir()
	s := newTestService(t, Options{OS: hostos.Darwin})
	j := &job{}
	long := filepath.Join(dir, strings.Repeat("g", 250)+".svg")
	if err := s.stage(j, long, []byte("<svg/>")); err != nil {
		t.Fatal(err)
	}
	defer discardStaged(j)
	base := filepath.Base(j.staged)
	if !strings.HasPrefix(base, ".spectra-svg-") || len(base) > 32 {
		t.Fatalf("staging name %q", base)
	}
	if fi, err := os.Stat(j.staged); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("staging file: %v, %v; want 0600", fi, err)
	}
}

// A result over the cap fails the job before the SVG replaces out, both on
// the cheap estimate and on the final encoded size.
func TestOversizedResultNeverReplacesSVG(t *testing.T) {
	for _, limit := range []int{10, len(fgReport) + 40} {
		out := writeTemp(t, "keep.svg", "previous")
		s := flamegraphService(t, Options{MaxResultBytes: limit}, nil, nil)
		rec := runFlamegraph(t, s, out)
		if rec.Status != StatusFailed || !strings.Contains(rec.Error, "limit") {
			t.Fatalf("limit %d: record = %+v", limit, rec)
		}
		if data, _ := os.ReadFile(out); string(data) != "previous" {
			t.Fatalf("limit %d: out replaced with %q", limit, data)
		}
		if names := dirEntries(t, filepath.Dir(out)); len(names) != 1 {
			t.Fatalf("limit %d: staging left behind: %v", limit, names)
		}
	}
}

// The estimate never exceeds the real encoding, so it cannot refuse a
// result that fits, and it grows with every expanded frame name.
func TestMinFlamegraphJSONIsALowerBound(t *testing.T) {
	long := strings.Repeat("Frame<&>", 64)
	rep := nativeprofile.FlamegraphReport{SVG: "/tmp/g.svg", StackCount: 2, Stacks: []flamegraph.Folded{
		{Frames: []string{"main", long}, Count: 3, FrameDetails: []binaryimages.Location{{Image: "App", ImageOffset: "0x10"}, {}}},
		{Frames: []string{"main", long, "leaf"}, Count: 1},
	}}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if est := minFlamegraphJSON(rep); est > len(raw) || est < 2*len(long) {
		t.Fatalf("estimate %d, encoded %d", est, len(raw))
	}
}

// A failed rename after the reservation is a job failure, not a completion.
func TestRenameFailureFailsJob(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.svg")
	if err := os.Mkdir(out, 0o700); err != nil { // a non-empty directory cannot be replaced
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := flamegraphService(t, Options{}, nil, nil)
	if rec := runFlamegraph(t, s, out); rec.Status != StatusFailed || !strings.Contains(rec.Error, "flamegraph: write "+out) {
		t.Fatalf("record = %+v", rec)
	}
}

// Cancellation that wins leaves out untouched and no staged file behind.
func TestCancelBeforePublishLeavesOutUntouched(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	s := flamegraphService(t, Options{}, release, started)
	dir := t.TempDir()
	out := filepath.Join(dir, "g.svg")
	v, err := call(t, s, start, StartParams{Kind: KindFlamegraph, PID: 1, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	waitSignal(t, started, "the capture to start")
	if v, err := call(t, s, cancel, IDParams{ID: v.(Job).ID}); err != nil || v.(Job).Status != StatusCancelled {
		t.Fatalf("cancel = %+v, %v", v, err)
	}
	close(release)
	s.Close(5 * time.Second)
	if names := dirEntries(t, dir); len(names) != 0 {
		t.Fatalf("dir holds %v after cancel", names)
	}
}

// publish refuses once the job is no longer working, and the staged file is
// discarded by the worker.
func TestPublishLosesToCancel(t *testing.T) {
	s := newTestService(t, Options{OS: hostos.Darwin})
	dir := t.TempDir()
	ctx, stop := context.WithCancel(context.Background())
	j := &job{cancel: stop, rec: Job{ID: "j", Kind: KindFlamegraph, Status: StatusWorking}}
	s.mu.Lock()
	s.jobs[j.rec.ID] = j
	s.mu.Unlock()
	if err := s.stage(j, filepath.Join(dir, "g.svg"), []byte("<svg/>")); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, s, cancel, IDParams{ID: j.rec.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.publish(ctx, j); !errors.Is(err, errPublishLost) {
		t.Fatalf("publish err = %v", err)
	}
	discardStaged(j)
	if names := dirEntries(t, dir); len(names) != 0 {
		t.Fatalf("dir holds %v", names)
	}
}

// After the reservation, cancel is refused: the job completes and the SVG
// is in place. The rename runs outside mu, so status reads do not wait on it.
func TestCancelAfterReservationIsRefused(t *testing.T) {
	s := newTestService(t, Options{OS: hostos.Darwin, CreatePerm: 0o644})
	dir := t.TempDir()
	out := filepath.Join(dir, "g.svg")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	j := &job{cancel: stop, rec: Job{ID: "j", Kind: KindFlamegraph, Status: StatusWorking}}
	s.mu.Lock()
	s.jobs[j.rec.ID] = j
	s.mu.Unlock()
	if err := s.stage(j, out, []byte("<svg/>")); err != nil {
		t.Fatal(err)
	}
	if err := s.publish(ctx, j); err != nil {
		t.Fatal(err)
	}
	v, err := call(t, s, cancel, IDParams{ID: j.rec.ID})
	if err != nil || v.(Job).Status != StatusWorking || ctx.Err() != nil {
		t.Fatalf("cancel after reservation = %+v, %v, ctx %v", v, err, ctx.Err())
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}
