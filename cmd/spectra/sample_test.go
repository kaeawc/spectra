package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func fakeSample(t *testing.T) sampleDeps {
	t.Helper()
	data, err := os.ReadFile("testdata/sample_report.txt")
	if err != nil {
		t.Fatal(err)
	}
	return sampleDeps{capture: func(_ context.Context, o process.SampleOptions) (process.SampleResult, error) {
		return process.SampleResult{PID: o.PID, TakenAt: time.Unix(1700000000, 0).UTC(),
			DurationMS: o.Duration.Milliseconds(), IntervalMS: o.IntervalMS, Output: string(data)}, nil
	}}
}

func TestSampleJSONShape(t *testing.T) {
	var out, errb bytes.Buffer
	for _, flagName := range []string{"-json", "--json"} {
		out.Reset()
		if code := runSampleWithIO([]string{flagName, "--duration", "2", "412"}, &out, &errb, fakeSample(t)); code != 0 {
			t.Fatalf("%s: code %d stderr %s", flagName, code, errb.String())
		}
		var rep sampleReport
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.PID != 412 || rep.Process != "Finder" || rep.DurationMS != 2000 || rep.IntervalMS != 10 {
			t.Errorf("header fields wrong: %+v", rep)
		}
		if rep.TotalSamples != 130 || rep.StackCount != 4 || len(rep.Stacks) != 4 {
			t.Errorf("totals wrong: %+v", rep)
		}
		top := rep.Stacks[0]
		if top.Count != 70 || top.Frames[len(top.Frames)-1] != "mach_msg2_trap" || top.Percent != 53.85 {
			t.Errorf("top stack wrong: %+v", top)
		}
		if !rep.MainThread.Found {
			t.Errorf("main thread not found: %+v", rep.MainThread)
		}
	}
}

func TestSampleTextUnchanged(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runSampleWithIO([]string{"412"}, &out, &errb, fakeSample(t)); code != 0 {
		t.Fatalf("code %d", code)
	}
	want, _ := os.ReadFile("testdata/sample_report.txt")
	if out.String() != string(want) {
		t.Errorf("text output changed:\n%s", out.String())
	}
}

func TestSampleJSONErrorWritesNothingToStdout(t *testing.T) {
	deps := sampleDeps{capture: func(context.Context, process.SampleOptions) (process.SampleResult, error) {
		return process.SampleResult{}, errors.New("no such process")
	}}
	var out, errb bytes.Buffer
	if code := runSampleWithIO([]string{"--json", "412"}, &out, &errb, deps); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if out.Len() != 0 {
		t.Errorf("stdout not empty: %q", out.String())
	}
	if !strings.Contains(errb.String(), "no such process") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestSampleUsageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runSampleWithIO([]string{"--json"}, &out, &errb, fakeSample(t)); code != 2 {
		t.Errorf("missing pid code = %d", code)
	}
	if code := runSampleWithIO([]string{"--json", "abc"}, &out, &errb, fakeSample(t)); code != 2 || out.Len() != 0 {
		t.Errorf("bad pid code = %d out=%q", code, out.String())
	}
}
