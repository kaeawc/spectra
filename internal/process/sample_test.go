package process

import (
	"context"
	"errors"
	"fmt"
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
	_, err := newTestSampler(&fakeCommandRunner{output: []byte("x")}, nil).
		Capture(context.Background(), SampleOptions{PID: 42})
	if !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("Capture on Linux: err = %v, want hostos.ErrUnsupported", err)
	}
}

type fakeCommandRunner struct {
	output []byte
	err    error
	calls  []fakeCommandCall
	files  *memReportFiles
}

type fakeCommandCall struct {
	name string
	args []string
}

// Run mimics sample(1) -file: the report goes to the named file, not stdout.
func (r *fakeCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, fakeCommandCall{name: name, args: append([]string(nil), args...)})
	if path := fileArg(args); path != "" && r.output != nil && r.files != nil {
		r.files.write(path, r.output)
	}
	return []byte("Sampling process...\n"), r.err
}

// memReportFiles is an in-memory ReportFiles that records removals.
type memReportFiles struct {
	files     map[string][]byte
	next      int
	createErr error
	readErr   error
	removed   []string
}

func newMemReportFiles() *memReportFiles {
	return &memReportFiles{files: map[string][]byte{}}
}

func (m *memReportFiles) CreateTemp(pattern string) (string, error) {
	if m.createErr != nil {
		return "", m.createErr
	}
	m.next++
	path := fmt.Sprintf("/mem/%d-%s", m.next, pattern)
	m.files[path] = nil
	return path, nil
}

func (m *memReportFiles) ReadFile(path string) ([]byte, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	data, ok := m.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (m *memReportFiles) Remove(path string) error {
	m.removed = append(m.removed, path)
	delete(m.files, path)
	return nil
}

func (m *memReportFiles) write(path string, data []byte) {
	if _, ok := m.files[path]; ok {
		m.files[path] = data
	}
}

func (m *memReportFiles) assertCleanedUp(t *testing.T) {
	t.Helper()
	if len(m.files) != 0 {
		t.Errorf("leftover report files: %v", m.files)
	}
	if m.next != len(m.removed) {
		t.Errorf("created %d report files, removed %d", m.next, len(m.removed))
	}
}

// newTestSampler wires runner and the sampler to one in-memory ReportFiles.
func newTestSampler(runner *fakeCommandRunner, store SampleStore) *Sampler {
	if runner.files == nil {
		runner.files = newMemReportFiles()
	}
	s := NewSampler(runner, store)
	s.files = runner.files
	return s
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
	runner := &fakeCommandRunner{output: []byte("Call graph:\n")}
	sampler := newTestSampler(runner, nil)

	res, err := sampler.Capture(context.Background(), SampleOptions{PID: 42})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if res.Output != "Call graph:\n" {
		t.Errorf("Output = %q, want the -file report", res.Output)
	}
	if got, want := fileArg(runner.calls[0].args), runner.files.removed[0]; got != want {
		t.Errorf("removed %q, want the -file path %q", want, got)
	}
	runner.files.assertCleanedUp(t)
}

func TestRunSampleReportRemovesTempFileOnRunError(t *testing.T) {
	runner := &fakeCommandRunner{output: []byte("partial"), err: errors.New("killed")}
	sampler := newTestSampler(runner, nil)

	if _, err := sampler.Capture(context.Background(), SampleOptions{PID: 42}); err == nil {
		t.Fatal("Capture succeeded, want error")
	}
	runner.files.assertCleanedUp(t)
}

func TestRunSampleReportRemovesTempFileOnReadError(t *testing.T) {
	files := newMemReportFiles()
	files.readErr = errors.New("permission denied")
	runner := &fakeCommandRunner{output: []byte("Call graph:\n"), files: files}

	_, err := newTestSampler(runner, nil).Capture(context.Background(), SampleOptions{PID: 42})
	if err == nil || !strings.Contains(err.Error(), "read sample report") || !errors.Is(err, files.readErr) {
		t.Fatalf("err = %v, want wrapped read sample report error", err)
	}
	files.assertCleanedUp(t)
}

func TestRunSampleReportCreateErrorSkipsRun(t *testing.T) {
	files := newMemReportFiles()
	files.createErr = errors.New("no space")
	runner := &fakeCommandRunner{output: []byte("x"), files: files}

	_, err := newTestSampler(runner, nil).Capture(context.Background(), SampleOptions{PID: 42})
	if !errors.Is(err, files.createErr) {
		t.Fatalf("err = %v, want create error", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("sample ran %d time(s) without a report file", len(runner.calls))
	}
}

func TestRunSampleReportReturnsStdoutOnError(t *testing.T) {
	files := newMemReportFiles()
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("sample: requires root"), errors.New("exit status 1")
	}
	_, stdout, err := RunSampleReport(context.Background(), files, run, "1", "1")
	if err == nil || string(stdout) != "sample: requires root" {
		t.Fatalf("stdout, err = %q, %v", stdout, err)
	}
	files.assertCleanedUp(t)
}

func TestOSReportFilesRemovesReport(t *testing.T) {
	dir := t.TempDir()
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return nil, os.WriteFile(fileArg(args), []byte("Call graph:\n"), 0o600)
	}
	report, _, err := RunSampleReport(context.Background(), OSReportFiles{Dir: dir}, run, "1", "1")
	if err != nil || string(report) != "Call graph:\n" {
		t.Fatalf("report, err = %q, %v", report, err)
	}
	assertDirEmpty(t, dir)
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
	sampler := newTestSampler(runner, store)
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
	sampler := newTestSampler(runner, nil)

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
	_, err := newTestSampler(&fakeCommandRunner{}, nil).Capture(context.Background(), SampleOptions{PID: 0})
	if err == nil {
		t.Fatal("Capture succeeded, want error")
	}
}

func TestSamplerCaptureWrapsRunnerError(t *testing.T) {
	_, err := newTestSampler(&fakeCommandRunner{err: errors.New("denied")}, nil).Capture(context.Background(), SampleOptions{PID: 42})
	if err == nil {
		t.Fatal("Capture succeeded, want error")
	}
	if got, want := err.Error(), "sample pid 42"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want it to contain %q", got, want)
	}
}

func TestSamplerCaptureReturnsStoreError(t *testing.T) {
	_, err := newTestSampler(&fakeCommandRunner{output: []byte("ok")}, &fakeSampleStore{err: errors.New("disk full")}).
		Capture(context.Background(), SampleOptions{PID: 42, Store: true})
	if err == nil {
		t.Fatal("Capture succeeded, want error")
	}
	if got, want := err.Error(), "store sample"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want it to contain %q", got, want)
	}
}
