package process

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// TestMain pins the host OS to Darwin for this package's tests. The
// process collectors are still macOS-shaped in this phase (ps/lsof/
// sample), so the success-path tests assume a Darwin host; tests that
// exercise other-OS behavior override this locally with hostos.SetForTest.
func TestMain(m *testing.M) {
	restore := hostos.SetForTest(hostos.Darwin)
	code := m.Run()
	restore()
	os.Exit(code)
}

func TestSamplerCaptureUnsupportedOffDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	_, err := NewSampler(&fakeCommandRunner{output: []byte("x")}, nil).
		Capture(context.Background(), SampleOptions{PID: 42})
	if !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("Capture on Linux: err = %v, want hostos.ErrUnsupported", err)
	}
}

type fakeCommandRunner struct {
	output []byte
	err    error
	calls  []fakeCommandCall
}

type fakeCommandCall struct {
	name string
	args []string
}

// Run mimics sample(1) -file: the report goes to the named file, not stdout.
func (r *fakeCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, fakeCommandCall{name: name, args: append([]string(nil), args...)})
	if path := fileArg(args); path != "" && r.output != nil {
		if err := os.WriteFile(path, r.output, 0o600); err != nil {
			return nil, err
		}
	}
	return []byte("Sampling process...\n"), r.err
}

func fileArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-file" {
			return args[i+1]
		}
	}
	return ""
}

// sampleArgs strips the per-run "-file <temp>" suffix.
func sampleArgs(t *testing.T, args []string) []string {
	t.Helper()
	n := len(args)
	if n < 2 || args[n-2] != "-file" {
		t.Fatalf("args = %v, want trailing -file <path>", args)
	}
	return args[:n-2]
}

func TestRunSampleReportRemovesTempFile(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeCommandRunner{output: []byte("Call graph:\n")}
	sampler := NewSampler(runner, nil)
	sampler.tempDir = dir

	res, err := sampler.Capture(context.Background(), SampleOptions{PID: 42})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if res.Output != "Call graph:\n" {
		t.Errorf("Output = %q, want the -file report", res.Output)
	}
	assertDirEmpty(t, dir)
}

func TestRunSampleReportRemovesTempFileOnError(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeCommandRunner{output: []byte("partial"), err: errors.New("killed")}
	sampler := NewSampler(runner, nil)
	sampler.tempDir = dir

	if _, err := sampler.Capture(context.Background(), SampleOptions{PID: 42}); err == nil {
		t.Fatal("Capture succeeded, want error")
	}
	assertDirEmpty(t, dir)
}

func TestRunSampleReportReturnsStdoutOnError(t *testing.T) {
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("sample: requires root"), errors.New("exit status 1")
	}
	_, stdout, err := RunSampleReport(context.Background(), t.TempDir(), run, "1", "1")
	if err == nil || string(stdout) != "sample: requires root" {
		t.Fatalf("stdout, err = %q, %v", stdout, err)
	}
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("leftover file %s", e.Name())
	}
}

type fakeSampleStore struct {
	samples []SampleResult
	err     error
}

func (s *fakeSampleStore) PutSample(_ context.Context, sample SampleResult) error {
	s.samples = append(s.samples, sample)
	return s.err
}

func TestSamplerCaptureRunsSampleCommand(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("Call graph:\n")}
	store := &fakeSampleStore{}
	sampler := NewSampler(runner, store)
	sampler.now = func() time.Time { return time.Unix(100, 0).UTC() }

	result, err := sampler.Capture(context.Background(), SampleOptions{
		PID:        4012,
		Duration:   5 * time.Second,
		IntervalMS: 20,
		Store:      true,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if result.PID != 4012 {
		t.Errorf("PID = %d, want 4012", result.PID)
	}
	if result.DurationMS != 5000 {
		t.Errorf("DurationMS = %d, want 5000", result.DurationMS)
	}
	if result.IntervalMS != 20 {
		t.Errorf("IntervalMS = %d, want 20", result.IntervalMS)
	}
	if result.Output != "Call graph:\n" {
		t.Errorf("Output = %q", result.Output)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	wantArgs := []string{"4012", "5", "20"}
	if runner.calls[0].name != "sample" || !reflect.DeepEqual(sampleArgs(t, runner.calls[0].args), wantArgs) {
		t.Errorf("runner call = %+v, want sample %v", runner.calls[0], wantArgs)
	}
	if len(store.samples) != 1 {
		t.Fatalf("stored samples = %d, want 1", len(store.samples))
	}
	if store.samples[0].Output != result.Output {
		t.Errorf("stored output = %q, want %q", store.samples[0].Output, result.Output)
	}
}

func TestSamplerCaptureDefaults(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("ok")}
	sampler := NewSampler(runner, nil)

	_, err := sampler.Capture(context.Background(), SampleOptions{PID: 99})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	wantArgs := []string{"99", "1", "10"}
	if !reflect.DeepEqual(sampleArgs(t, runner.calls[0].args), wantArgs) {
		t.Errorf("runner args = %v, want %v", runner.calls[0].args, wantArgs)
	}
}

func TestSamplerCaptureRejectsInvalidPID(t *testing.T) {
	_, err := NewSampler(&fakeCommandRunner{}, nil).Capture(context.Background(), SampleOptions{PID: 0})
	if err == nil {
		t.Fatal("Capture succeeded, want error")
	}
}

func TestSamplerCaptureWrapsRunnerError(t *testing.T) {
	_, err := NewSampler(&fakeCommandRunner{err: errors.New("denied")}, nil).Capture(context.Background(), SampleOptions{PID: 42})
	if err == nil {
		t.Fatal("Capture succeeded, want error")
	}
	if got, want := err.Error(), "sample pid 42"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want it to contain %q", got, want)
	}
}

func TestSamplerCaptureReturnsStoreError(t *testing.T) {
	_, err := NewSampler(&fakeCommandRunner{output: []byte("ok")}, &fakeSampleStore{err: errors.New("disk full")}).
		Capture(context.Background(), SampleOptions{PID: 42, Store: true})
	if err == nil {
		t.Fatal("Capture succeeded, want error")
	}
	if got, want := err.Error(), "store sample"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want it to contain %q", got, want)
	}
}
