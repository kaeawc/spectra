package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/helperclient"
)

func TestHelperInstallUserPrefersSudoUser(t *testing.T) {
	env := map[string]string{"SUDO_USER": "alice", "USER": "root"}
	got := helperInstallUser(func(key string) string { return env[key] })
	if got != "alice" {
		t.Fatalf("helperInstallUser = %q, want alice", got)
	}
}

func TestHelperInstallUserFallsBackToUser(t *testing.T) {
	env := map[string]string{"USER": "bob"}
	got := helperInstallUser(func(key string) string { return env[key] })
	if got != "bob" {
		t.Fatalf("helperInstallUser = %q, want bob", got)
	}
}

func TestHelperNewsyslogContent(t *testing.T) {
	want := "/var/log/spectra-helper.log\troot:wheel\t640\t7\t1024\t*\tJ\n"
	if helperNewsyslogContent != want {
		t.Fatalf("helperNewsyslogContent = %q, want %q", helperNewsyslogContent, want)
	}
}

func TestInstallRootTextFileCopiesTemporaryContent(t *testing.T) {
	var gotPattern, gotContent string
	cleaned := false
	write := func(pattern, content string) (string, func(), error) {
		gotPattern = pattern
		gotContent = content
		return "/tmp/spectra-helper-test", func() { cleaned = true }, nil
	}

	var calls [][]string
	run := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	if err := installRootTextFile("/etc/newsyslog.d/spectra-helper.conf", helperNewsyslogContent, run, write); err != nil {
		t.Fatal(err)
	}
	if gotPattern != "spectra-helper-*" {
		t.Fatalf("pattern = %q", gotPattern)
	}
	if gotContent != helperNewsyslogContent {
		t.Fatalf("content = %q", gotContent)
	}
	if !cleaned {
		t.Fatal("temporary file cleanup was not called")
	}
	wantCalls := [][]string{{"cp", "/tmp/spectra-helper-test", "/etc/newsyslog.d/spectra-helper.conf"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
}

func TestInstallHelperRunsExpectedRootOperations(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.env["SUDO_USER"] = "alice"
	if err := installHelper(fake.deps(), helperInstallOptions{}); err != nil {
		t.Fatal(err)
	}

	helperSrc := filepath.Join(filepath.Dir(fake.executablePath), "spectra-helper")
	wantRuns := [][]string{
		{"dseditgroup", "-o", "read", helperGroup},
		{"dseditgroup", "-o", "edit", "-a", "alice", "-t", "user", helperGroup},
		{"mkdir", "-p", "/Library/PrivilegedHelperTools"},
		{"cp", helperSrc, helperBinaryDest},
		{"chown", "root:wheel", helperBinaryDest},
		{"chmod", "755", helperBinaryDest},
		{"cp", "/tmp/spectra-helper-1", helperPlist},
		{"cp", "/tmp/spectra-helper-2", helperNewsyslogConf},
		{"launchctl", "load", "-w", helperPlist},
	}
	if !reflect.DeepEqual(fake.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v", fake.runs, wantRuns)
	}
	wantTemps := []string{helperPlistContent, helperNewsyslogContent}
	if !slices.Equal(fake.tempContents, wantTemps) {
		t.Fatalf("temp contents = %q, want %q", fake.tempContents, wantTemps)
	}
	if fake.cleanups != 2 {
		t.Fatalf("cleanups = %d, want 2", fake.cleanups)
	}
}

func TestInstallHelperCreatesMissingGroup(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.runErrs["dseditgroup\x00-o\x00read\x00"+helperGroup] = errors.New("no group")
	if err := installHelper(fake.deps(), helperInstallOptions{}); err != nil {
		t.Fatal(err)
	}
	wantPrefix := [][]string{
		{"dseditgroup", "-o", "read", helperGroup},
		{"dseditgroup", "-o", "create", helperGroup},
	}
	if !reflect.DeepEqual(fake.runs[:2], wantPrefix) {
		t.Fatalf("runs prefix = %v, want %v", fake.runs[:2], wantPrefix)
	}
}

func TestInstallHelperSkipsRootUserGroupEdit(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.env["SUDO_USER"] = "root"
	if err := installHelper(fake.deps(), helperInstallOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, run := range fake.runs {
		if slices.Contains(run, "-a") {
			t.Fatalf("group edit should be skipped for root user, got runs %v", fake.runs)
		}
	}
}

func TestInstallHelperReportsMissingHelperBinary(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.statErr = os.ErrNotExist
	err := installHelper(fake.deps(), helperInstallOptions{})
	if err == nil {
		t.Fatal("installHelper succeeded with missing helper binary")
	}
	if !strings.Contains(err.Error(), "spectra-helper binary not found") {
		t.Fatalf("error = %v", err)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("runs = %v, want none", fake.runs)
	}
}

func TestUninstallHelperRunsExpectedRootOperations(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	if err := uninstallHelper(fake.deps()); err != nil {
		t.Fatal(err)
	}
	wantRuns := [][]string{
		{"launchctl", "unload", "-w", helperPlist},
		{"rm", "-f", helperPlist},
		{"rm", "-f", helperNewsyslogConf},
		{"rm", "-f", helperBinaryDest},
	}
	if !reflect.DeepEqual(fake.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v", fake.runs, wantRuns)
	}
}

func TestInstallHelperCanRequireDeveloperIDSignature(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.checkOut["codesign\x00-dv\x00--verbose=4\x00"+fake.helperSrc()] = "Authority=Developer ID Application: Example, Inc. (ABCDE12345)\n"
	if err := installHelper(fake.deps(), helperInstallOptions{RequireSigned: true}); err != nil {
		t.Fatal(err)
	}
	wantChecks := [][]string{
		{"codesign", "-dv", "--verbose=4", fake.helperSrc()},
		{"codesign", "--verify", "--strict", "--verbose=2", fake.helperSrc()},
	}
	if !reflect.DeepEqual(fake.checks, wantChecks) {
		t.Fatalf("checks = %v, want %v", fake.checks, wantChecks)
	}
}

func TestInstallHelperRejectsNonDeveloperIDSignature(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.checkOut["codesign\x00-dv\x00--verbose=4\x00"+fake.helperSrc()] = "Authority=Apple Development: Example\n"
	err := installHelper(fake.deps(), helperInstallOptions{RequireSigned: true})
	if err == nil {
		t.Fatal("installHelper accepted non-Developer ID signature")
	}
	if !strings.Contains(err.Error(), "not with a Developer ID Application certificate") {
		t.Fatalf("error = %v", err)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("runs = %v, want none", fake.runs)
	}
}

func TestInstallHelperRequireSignedEnv(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.env["SPECTRA_REQUIRE_SIGNED_HELPER"] = "true"
	fake.checkOut["codesign\x00-dv\x00--verbose=4\x00"+fake.helperSrc()] = "Authority=Developer ID Application: Example, Inc. (ABCDE12345)\n"
	if err := installHelper(fake.deps(), helperInstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(fake.checks) != 2 {
		t.Fatalf("checks = %v, want codesign checks", fake.checks)
	}
}

func TestHelperStatusUsesInjectedClient(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.statusClient.available = true
	fake.statusClient.health = map[string]any{"ok": true}
	if err := helperStatus(fake.deps()); err != nil {
		t.Fatal(err)
	}
	if !fake.statusClient.healthCalled {
		t.Fatal("Health was not called")
	}
}

func TestHelperStatusUnavailable(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	err := helperStatus(fake.deps())
	if err == nil {
		t.Fatal("helperStatus succeeded with unavailable helper")
	}
	if !errors.Is(err, helperclient.ErrHelperUnavailable) {
		t.Fatalf("error = %v, want helper unavailable", err)
	}
}

func TestSudoCommandAllowedCoversHelperManagementCommands(t *testing.T) {
	for _, name := range []string{"chmod", "chown", "cp", "dseditgroup", "launchctl", "mkdir", "rm"} {
		if !sudoCommandAllowed(name) {
			t.Fatalf("sudoCommandAllowed(%q) = false", name)
		}
	}
}

func TestTruthyEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		if !truthyEnv(v) {
			t.Fatalf("truthyEnv(%q) = false", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off"} {
		if truthyEnv(v) {
			t.Fatalf("truthyEnv(%q) = true", v)
		}
	}
}

func TestSudoRunRejectsNonAllowlistedCommand(t *testing.T) {
	err := sudoRun("sh", "-c", "echo should-not-run")
	if err == nil {
		t.Fatal("sudoRun accepted a non-allowlisted command")
	}
	if !strings.Contains(err.Error(), `sudo command "sh" is not allowlisted`) {
		t.Fatalf("error = %v", err)
	}
}

type fakeHelperInstallDeps struct {
	executablePath string
	env            map[string]string
	runs           [][]string
	runErrs        map[string]error
	checks         [][]string
	checkOut       map[string]string
	checkErrs      map[string]error
	statErr        error
	tempContents   []string
	cleanups       int
	statusClient   *fakeHelperStatusClient
}

func newFakeHelperInstallDeps(t *testing.T) *fakeHelperInstallDeps {
	t.Helper()
	return &fakeHelperInstallDeps{
		executablePath: filepath.Join(t.TempDir(), "spectra"),
		env:            map[string]string{"USER": "alice"},
		runErrs:        map[string]error{},
		checkOut:       map[string]string{},
		checkErrs:      map[string]error{},
		statusClient:   &fakeHelperStatusClient{},
	}
}

func (f *fakeHelperInstallDeps) helperSrc() string {
	return filepath.Join(filepath.Dir(f.executablePath), "spectra-helper")
}

func (f *fakeHelperInstallDeps) deps() helperInstallDeps {
	return helperInstallDeps{
		executable: func() (string, error) {
			return f.executablePath, nil
		},
		stat: func(string) (os.FileInfo, error) {
			if f.statErr != nil {
				return nil, f.statErr
			}
			return nil, nil
		},
		getenv: func(key string) string {
			return f.env[key]
		},
		runCheck: func(name string, args ...string) (string, error) {
			call := append([]string{name}, args...)
			f.checks = append(f.checks, slices.Clone(call))
			key := strings.Join(call, "\x00")
			return f.checkOut[key], f.checkErrs[key]
		},
		runRoot: func(name string, args ...string) error {
			call := append([]string{name}, args...)
			f.runs = append(f.runs, slices.Clone(call))
			return f.runErrs[strings.Join(call, "\x00")]
		},
		writeTemp: func(_ string, content string) (string, func(), error) {
			f.tempContents = append(f.tempContents, content)
			path := "/tmp/spectra-helper-" + string(rune('0'+len(f.tempContents)))
			return path, func() { f.cleanups++ }, nil
		},
		client: func() helperStatusClient {
			return f.statusClient
		},
	}
}

type fakeHelperStatusClient struct {
	available    bool
	health       map[string]any
	healthErr    error
	healthCalled bool
}

func (f *fakeHelperStatusClient) Available() bool {
	return f.available
}

func (f *fakeHelperStatusClient) Health() (map[string]any, error) {
	f.healthCalled = true
	return f.health, f.healthErr
}
