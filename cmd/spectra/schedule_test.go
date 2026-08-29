package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeAgent struct {
	written map[string][]byte
	removed []string
	runs    [][]string
	printOK bool
}

func newFakeAgent() *fakeAgent { return &fakeAgent{written: map[string][]byte{}} }

func (f *fakeAgent) deps() daemonAgentDeps {
	return daemonAgentDeps{
		executable: func() (string, error) { return "/usr/local/bin/spectra", nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		uid:        func() string { return "501" },
		mkdirAll:   func(string, os.FileMode) error { return nil },
		writeFile:  func(p string, b []byte, _ os.FileMode) error { f.written[p] = b; return nil },
		remove:     func(p string) error { f.removed = append(f.removed, p); return nil },
		run:        func(a ...string) error { f.runs = append(f.runs, a); return nil },
		output: func(a ...string) ([]byte, error) {
			if f.printOK {
				return []byte("dev.spectra.snapshot => loaded"), nil
			}
			return nil, errors.New("not loaded")
		},
	}
}

func TestScheduleInstallWritesPlistAndLoads(t *testing.T) {
	f := newFakeAgent()
	var out, errBuf bytes.Buffer
	if code := runScheduleWithIO([]string{"install", "--interval", "30m"}, &out, &errBuf, f.deps()); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	plist := f.written["/home/tester/Library/LaunchAgents/dev.spectra.snapshot.plist"]
	if plist == nil {
		t.Fatalf("plist not written; wrote %v", f.written)
	}
	s := string(plist)
	if !strings.Contains(s, "<integer>1800</integer>") {
		t.Errorf("StartInterval should be 1800s; got:\n%s", s)
	}
	if !strings.Contains(s, "<string>snapshot</string>") {
		t.Errorf("ProgramArguments should run snapshot; got:\n%s", s)
	}
	// launchctl bootstrap should have run
	var bootstrapped bool
	for _, r := range f.runs {
		if len(r) > 0 && r[0] == "bootstrap" {
			bootstrapped = true
		}
	}
	if !bootstrapped {
		t.Errorf("expected a launchctl bootstrap; runs=%v", f.runs)
	}
}

func TestScheduleInstallNoLoad(t *testing.T) {
	f := newFakeAgent()
	var out, errBuf bytes.Buffer
	if code := runScheduleWithIO([]string{"install", "--interval", "1h", "--no-load"}, &out, &errBuf, f.deps()); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(f.runs) != 0 {
		t.Errorf("--no-load must not call launchctl; runs=%v", f.runs)
	}
	if len(f.written) != 1 {
		t.Errorf("expected exactly the plist written; got %v", f.written)
	}
}

func TestScheduleInstallRejectsShortInterval(t *testing.T) {
	f := newFakeAgent()
	var out, errBuf bytes.Buffer
	if code := runScheduleWithIO([]string{"install", "--interval", "10s"}, &out, &errBuf, f.deps()); code != 2 {
		t.Fatalf("exit = %d, want 2 for a sub-minute interval", code)
	}
}

func TestScheduleUninstall(t *testing.T) {
	f := newFakeAgent()
	var out, errBuf bytes.Buffer
	if code := runScheduleWithIO([]string{"uninstall"}, &out, &errBuf, f.deps()); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(f.removed) != 1 || !strings.HasSuffix(f.removed[0], "dev.spectra.snapshot.plist") {
		t.Errorf("expected the plist removed; removed=%v", f.removed)
	}
}

func TestSchedulePrintPlistWritesNothing(t *testing.T) {
	f := newFakeAgent()
	var out, errBuf bytes.Buffer
	if code := runScheduleWithIO([]string{"print-plist", "--interval", "15m"}, &out, &errBuf, f.deps()); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(f.written) != 0 || len(f.runs) != 0 {
		t.Errorf("print-plist must not write or load; written=%v runs=%v", f.written, f.runs)
	}
	if !strings.Contains(out.String(), "<integer>900</integer>") {
		t.Errorf("printed plist should carry StartInterval 900; got:\n%s", out.String())
	}
}

func TestScheduleStatusNotLoaded(t *testing.T) {
	f := newFakeAgent()
	var out, errBuf bytes.Buffer
	runScheduleWithIO([]string{"status"}, &out, &errBuf, f.deps())
	if !strings.Contains(out.String(), "not loaded") {
		t.Errorf("out = %q", out.String())
	}
}

func TestScheduleUnknownSub(t *testing.T) {
	f := newFakeAgent()
	var out, errBuf bytes.Buffer
	if code := runScheduleWithIO([]string{"bogus"}, &out, &errBuf, f.deps()); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
