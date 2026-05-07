package process

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	output []byte
	err    error
	calls  []fakeCommandCall
}

type fakeCommandCall struct {
	name string
	args []string
}

func (r *fakeCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, fakeCommandCall{name: name, args: append([]string(nil), args...)})
	return r.output, r.err
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
	if runner.calls[0].name != "sample" || !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
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
	if !reflect.DeepEqual(runner.calls[0].args, wantArgs) {
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
