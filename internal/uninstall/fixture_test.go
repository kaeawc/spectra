package uninstall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// fakeRunner answers by exact command line; anything unscripted exits 1.
type fakeRunner struct {
	mu    sync.Mutex
	resp  map[string]proc.Result
	calls []string
}

func newFakeRunner() *fakeRunner { return &fakeRunner{resp: map[string]proc.Result{}} }

func (f *fakeRunner) on(stdout, stderr string, name string, args ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resp[strings.Join(append([]string{name}, args...), " ")] = proc.Result{Stdout: []byte(stdout), Stderr: []byte(stderr)}
}

func (f *fakeRunner) Run(_ context.Context, c proc.Cmd) (proc.Result, error) {
	key := strings.Join(append([]string{c.Name}, c.Args...), " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if r, ok := f.resp[key]; ok {
		return r, nil
	}
	return proc.Result{ExitCode: 1}, nil
}

func (f *fakeRunner) called(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// ownerFS reports paths under rootOwned as owned by root, so tests can model
// system files without chown.
type ownerFS struct {
	OSFS
	rootOwned []string
}

func (f ownerFS) Meta(path string) (Meta, error) {
	m, err := f.OSFS.Meta(path)
	if err == nil && underAny(path, f.rootOwned) {
		m.UID = 0
		m.GID = 0
	}
	return m, err
}

type fixture struct {
	t        *testing.T
	root     string
	home     string
	lib      string
	sysLib   string
	apps     string
	receipts string
	trash    string
	runner   *fakeRunner
	procs    func() []process.Info
	now      time.Time
	opts     Options
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t: t, root: root, home: filepath.Join(root, "home"),
		sysLib: filepath.Join(root, "Library"), apps: filepath.Join(root, "Applications"),
		receipts: filepath.Join(root, "var", "db", "receipts"), runner: newFakeRunner(),
		now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
	}
	f.lib = filepath.Join(f.home, "Library")
	f.trash = filepath.Join(f.home, ".Trash")
	for _, d := range []string{f.lib, f.trash, f.sysLib, f.apps, f.receipts} {
		f.mkdir(d)
	}
	f.procs = func() []process.Info { return nil }
	f.opts = Options{
		Home: f.home, AppRoots: []string{f.apps}, SystemLibrary: f.sysLib, Receipts: f.receipts,
		SystemRoots:    []string{filepath.Join(root, "System")},
		SystemAppRoots: []string{filepath.Join(root, "System", "Applications"), filepath.Join(root, "System", "Applications", "Utilities")},
		UID:            int64(os.Getuid()), GIDs: []int64{int64(os.Getgid())},
		Runner: f.runner, FS: ownerFS{rootOwned: []string{f.sysLib, filepath.Join(root, "var"), filepath.Join(root, "System")}},
		Procs:   func(context.Context) []process.Info { return f.procs() },
		Now:     func() time.Time { return f.now },
		Sleep:   func(context.Context, time.Duration) { f.now = f.now.Add(time.Second) },
		SelfPID: os.Getpid(),
		OS:      hostos.Darwin,
	}
	return f
}

func (f *fixture) mkdir(p string) string {
	f.t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) write(p, content string) string {
	f.t.Helper()
	f.mkdir(filepath.Dir(p))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// userPath returns a path under ~/Library.
func (f *fixture) userPath(parts ...string) string {
	return filepath.Join(append([]string{f.lib}, parts...)...)
}

func plistXML(kv map[string]string, extra string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>`)
	for k, v := range kv {
		fmt.Fprintf(&b, "<key>%s</key><string>%s</string>", k, v)
	}
	b.WriteString(extra)
	b.WriteString(`</dict></plist>`)
	return b.String()
}

// app writes a minimal bundle at dir/name.app with the given bundle id.
func (f *fixture) app(dir, name, id string, extra string) string {
	f.t.Helper()
	p := filepath.Join(dir, name+".app")
	f.write(filepath.Join(p, "Contents", "Info.plist"), plistXML(map[string]string{
		"CFBundleIdentifier": id, "CFBundleName": name, "CFBundleShortVersionString": "1.0",
	}, extra))
	f.write(filepath.Join(p, "Contents", "MacOS", name), "bin")
	return p
}

func (f *fixture) helper(bundle, sub, name, id string) {
	f.t.Helper()
	p := filepath.Join(bundle, "Contents", sub, name)
	f.write(filepath.Join(p, "Contents", "Info.plist"), plistXML(map[string]string{"CFBundleIdentifier": id}, ""))
}

func (f *fixture) signing(bundle, team string, groups ...string) {
	f.runner.on("", "Identifier=x\nTeamIdentifier="+team+"\n", "codesign", "-dv", bundle)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>com.apple.security.application-groups</key><array>`)
	for _, g := range groups {
		fmt.Fprintf(&b, "<string>%s</string>", g)
	}
	b.WriteString(`</array></dict></plist>`)
	f.runner.on(b.String(), "", "codesign", "-d", "--entitlements", ":-", bundle)
}

func (f *fixture) launchd(path, label, program string, extra string) string {
	kv := map[string]string{"Label": label}
	if program != "" {
		kv["Program"] = program
	}
	return f.write(path, plistXML(kv, extra))
}

func (f *fixture) plan(target string) Plan {
	f.t.Helper()
	p, err := BuildPlan(context.Background(), f.opts, target)
	if err != nil {
		f.t.Fatalf("BuildPlan: %v", err)
	}
	return p
}

func itemAt(p Plan, path string) (Item, bool) {
	for _, it := range p.Items {
		if it.Path == path {
			return it, true
		}
	}
	return Item{}, false
}

func (f *fixture) mustItem(p Plan, path string, kind Kind, reason Reason, action Action) Item {
	f.t.Helper()
	it, ok := itemAt(p, path)
	if !ok {
		f.t.Fatalf("plan missing %s; items: %v", path, paths(p))
	}
	if it.Kind != kind || it.Reason != reason || it.Action != action {
		f.t.Fatalf("%s = %s/%s/%s, want %s/%s/%s", path, it.Kind, it.Reason, it.Action, kind, reason, action)
	}
	return it
}

func (f *fixture) mustNotItem(p Plan, path string) {
	f.t.Helper()
	if _, ok := itemAt(p, path); ok {
		f.t.Fatalf("plan unexpectedly includes %s", path)
	}
}

func paths(p Plan) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Path)
	}
	return out
}

func existsPath(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
