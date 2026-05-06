package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDaemonAgentServeArgs(t *testing.T) {
	opts := daemonAgentOptions{
		SockPath:       "/tmp/spectra.sock",
		TCPAddr:        "127.0.0.1:7878",
		AllowRemote:    true,
		TsnetEnabled:   true,
		TsnetAddr:      ":7879",
		TsnetHostname:  "work-mac",
		TsnetStateDir:  "/tmp/spectra-tsnet",
		TsnetEphemeral: true,
		TsnetTags:      "tag:engineer,tag:spectra",
		LogFile:        "/tmp/spectra.jsonl",
	}
	got := opts.serveArgs()
	want := []string{
		"serve",
		"--sock", "/tmp/spectra.sock",
		"--tcp", "127.0.0.1:7878",
		"--allow-remote",
		"--tsnet",
		"--tsnet-addr", ":7879",
		"--tsnet-hostname", "work-mac",
		"--tsnet-state-dir", "/tmp/spectra-tsnet",
		"--tsnet-ephemeral",
		"--tsnet-tags", "tag:engineer,tag:spectra",
		"--log-file", "/tmp/spectra.jsonl",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("serveArgs = %v, want %v", got, want)
	}
}

func TestDaemonLaunchAgentPlistEscapesArguments(t *testing.T) {
	plist := daemonLaunchAgentPlist(
		"/tmp/Spectra & Tool/spectra",
		[]string{"serve", "--log-file", "/tmp/a&b.jsonl"},
		"/tmp/out.log",
		"/tmp/err.log",
	)
	for _, want := range []string{
		"<string>/tmp/Spectra &amp; Tool/spectra</string>",
		"<string>/tmp/a&amp;b.jsonl</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>dev.spectra.daemon</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
}

func TestInstallDaemonAgentNoLoad(t *testing.T) {
	fake := newFakeDaemonAgentDeps(t)
	opts := daemonAgentOptions{NoLoad: true, NoLogFile: true}
	plistPath, err := installDaemonAgent(opts, fake.deps())
	if err != nil {
		t.Fatal(err)
	}
	if plistPath != filepath.Join(fake.home, "Library", "LaunchAgents", daemonAgentLabel+".plist") {
		t.Fatalf("plist path = %q", plistPath)
	}
	if len(fake.runs) != 0 {
		t.Fatalf("launchctl calls = %v, want none", fake.runs)
	}
	data := string(fake.files[plistPath])
	if !strings.Contains(data, "<string>/usr/local/bin/spectra</string>") {
		t.Fatalf("plist = %s", data)
	}
	if !strings.Contains(data, "<string>--no-log-file</string>") {
		t.Fatalf("plist missing --no-log-file:\n%s", data)
	}
}

func TestInstallDaemonAgentBootstrapsLaunchd(t *testing.T) {
	fake := newFakeDaemonAgentDeps(t)
	plistPath, err := installDaemonAgent(daemonAgentOptions{}, fake.deps())
	if err != nil {
		t.Fatal(err)
	}
	wantRuns := [][]string{
		{"bootout", "gui/501", plistPath},
		{"bootstrap", "gui/501", plistPath},
		{"enable", "gui/501/" + daemonAgentLabel},
		{"kickstart", "-k", "gui/501/" + daemonAgentLabel},
	}
	if !equalStringSlices(fake.runs, wantRuns) {
		t.Fatalf("runs = %v, want %v", fake.runs, wantRuns)
	}
}

func TestUninstallDaemonAgent(t *testing.T) {
	fake := newFakeDaemonAgentDeps(t)
	plistPath := filepath.Join(fake.home, "Library", "LaunchAgents", daemonAgentLabel+".plist")
	fake.files[plistPath] = []byte("plist")
	got, err := uninstallDaemonAgent(fake.deps())
	if err != nil {
		t.Fatal(err)
	}
	if got != plistPath {
		t.Fatalf("plist path = %q", got)
	}
	if _, ok := fake.files[plistPath]; ok {
		t.Fatalf("plist was not removed")
	}
	if !equalStringSlices(fake.runs, [][]string{{"bootout", "gui/501", plistPath}}) {
		t.Fatalf("runs = %v", fake.runs)
	}
}

type fakeDaemonAgentDeps struct {
	home  string
	files map[string][]byte
	dirs  []string
	runs  [][]string
}

func newFakeDaemonAgentDeps(t *testing.T) *fakeDaemonAgentDeps {
	t.Helper()
	return &fakeDaemonAgentDeps{
		home:  filepath.Join(t.TempDir(), "home"),
		files: map[string][]byte{},
	}
}

func (f *fakeDaemonAgentDeps) deps() daemonAgentDeps {
	return daemonAgentDeps{
		executable: func() (string, error) { return "/usr/local/bin/spectra", nil },
		homeDir:    func() (string, error) { return f.home, nil },
		uid:        func() string { return "501" },
		mkdirAll: func(path string, _ os.FileMode) error {
			f.dirs = append(f.dirs, path)
			return nil
		},
		writeFile: func(path string, data []byte, _ os.FileMode) error {
			f.files[path] = slices.Clone(data)
			return nil
		},
		remove: func(path string) error {
			if _, ok := f.files[path]; !ok {
				return os.ErrNotExist
			}
			delete(f.files, path)
			return nil
		},
		run: func(args ...string) error {
			f.runs = append(f.runs, slices.Clone(args))
			return nil
		},
		output: func(args ...string) ([]byte, error) {
			if len(args) == 0 {
				return nil, errors.New("missing launchctl args")
			}
			return []byte(strings.Join(args, " ")), nil
		},
	}
}

func equalStringSlices(a [][]string, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !slices.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}
