package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/procdiag"
)

func fakeInspector(rep procdiag.Report, err error) procdiagInspector {
	return func(context.Context, int, procdiag.Options) (procdiag.Report, error) { return rep, err }
}

func TestProcdiagReportsFindingsAndExit3(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	rep := procdiag.Report{PID: 9, Comm: "leaky", Verdict: procdiag.KindFDGrowth, IntervalSec: 1, StateCounts: map[string]int{"S": 1},
		Findings: []procdiag.Finding{{Kind: procdiag.KindFDGrowth, Severity: procdiag.SeverityWarning, Summary: "fds climbing", Evidence: []string{"+98 fds"}}}}
	var out, errb bytes.Buffer
	if code := runProcdiagWithIO([]string{"9"}, &out, &errb, fakeInspector(rep, nil)); code != 3 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	for _, want := range []string{"pid 9 (leaky) — fd-growth", "[warning] fd-growth: fds climbing", "+98 fds"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestProcdiagInfoOnlyExitsZeroJSON(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	rep := procdiag.Report{PID: 9, Verdict: procdiag.KindIdle, Findings: []procdiag.Finding{{Kind: "x", Severity: procdiag.SeverityInfo}}}
	var out, errb bytes.Buffer
	if code := runProcdiagWithIO([]string{"--json", "9"}, &out, &errb, fakeInspector(rep, nil)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var got procdiag.Report
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.PID != 9 {
		t.Fatalf("json: %v %s", err, out.String())
	}
}

func TestProcdiagErrors(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	cases := []struct {
		args []string
		err  error
		code int
		msg  string
	}{
		{[]string{}, nil, 2, "usage"},
		{[]string{"abc"}, nil, 2, "invalid PID"},
		{[]string{"--interval", "0s", "5"}, nil, 2, "positive"},
		{[]string{"5"}, fmt.Errorf("pid 5: %w", procdiag.ErrNoProcess), 1, "no such process"},
		{[]string{"5"}, errors.New("boom"), 1, "boom"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if code := runProcdiagWithIO(c.args, &out, &errb, fakeInspector(procdiag.Report{}, c.err)); code != c.code || !strings.Contains(errb.String(), c.msg) {
			t.Errorf("%v: code=%d stderr=%q", c.args, code, errb.String())
		}
	}
}

func TestProcdiagUnsupportedOnDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	var out, errb bytes.Buffer
	if code := runProcdiagWithIO([]string{"5"}, &out, &errb, fakeInspector(procdiag.Report{}, nil)); code != 1 || !strings.Contains(errb.String(), "unsupported") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func pressureFixture(t *testing.T, memFull string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"cpu":    "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
		"memory": "some avg10=40.00 avg60=20.00 avg300=5.00 total=9\nfull avg10=" + memFull + " avg60=0.00 avg300=0.00 total=9\n",
		"io":     "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
	} {
		p := filepath.Join(root, "pressure", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPressureFlagsThrashing(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	proc := pressureFixture(t, "15.00")
	var out, errb bytes.Buffer
	if code := runPressureWithIO(nil, &out, &errb, proc, t.TempDir()); code != 3 {
		t.Fatalf("exit = %d stderr=%s", code, errb.String())
	}
	for _, want := range []string{"memory-pressure", "memory-stall", "critical"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
}

func TestPressureMissingPSIIsANoteNotAFailure(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	var out, errb bytes.Buffer
	if code := runPressureWithIO([]string{"--json"}, &out, &errb, t.TempDir(), t.TempDir()); code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errb.String())
	}
	var rep pressureReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || rep.PSI != nil || len(rep.Notes) != 1 || rep.Findings == nil {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}

func TestPressureUnsupportedOnDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	var out, errb bytes.Buffer
	if code := runPressureWithIO(nil, &out, &errb, "/proc", "/sys/fs/cgroup"); code != 1 || !strings.Contains(errb.String(), "unsupported") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}
