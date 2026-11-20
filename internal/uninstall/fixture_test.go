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
	mu     sync.Mutex
	resp   map[string]proc.Result
	prefix map[string]proc.Result
	calls  []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{resp: map[string]proc.Result{}, prefix: map[string]proc.Result{}}
}

// onPrefix answers any command line starting with name and args that has
// no exact answer; the longest prefix wins.
func (f *fakeRunner) onPrefix(res proc.Result, name string, args ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefix[strings.Join(append([]string{name}, args...), " ")] = res
}

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
	best, found := "", false
	for p := range f.prefix {
		if strings.HasPrefix(key, p) && len(p) >= len(best) {
			best, found = p, true
		}
	}
	if found {
		return f.prefix[best], nil
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
	// coreServices is the fixture's /System/Library/CoreServices; finder
	// is the canary bundle in it.
	coreServices string
	finder       string
	runner       *fakeRunner
	procs        func() []process.Info
	now          time.Time
	opts         Options
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
	f.coreServices = filepath.Join(root, "System", "Library", "CoreServices")
	f.finder = f.app(f.coreServices, "Finder", "com.apple.finder", "")
	// Spotlight answers, with only the canary, unless a test says otherwise.
	f.spotlight()
	// launchctl bootout succeeds unless a test says otherwise.
	f.runner.onPrefix(proc.Result{}, "launchctl", "bootout")
	f.opts = Options{
		Home: f.home, AppRoots: []string{f.apps}, SystemLibrary: f.sysLib, Receipts: f.receipts,
		SystemRoots: []string{filepath.Join(root, "System")},
		SystemAppRoots: []string{filepath.Join(root, "System", "Applications"), f.coreServices,
			filepath.Join(f.coreServices, "Applications")},
		UID: int64(os.Getuid()), GIDs: []int64{int64(os.Getgid())},
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

// signing scripts a valid signature with team (verify passes when team is
// set) and the given app groups.
func (f *fixture) signing(bundle, team string, groups ...string) {
	if team != "" {
		f.runner.on("", "", "codesign", "--verify", "--strict", bundle)
	}
	f.signingUnverified(bundle, team, groups...)
}

// signingUnverified scripts `codesign -dv` and the entitlements only:
// `codesign --verify` fails, as for a tampered or forged signature.
func (f *fixture) signingUnverified(bundle, team string, groups ...string) {
	f.runner.on("", "Identifier=x\nTeamIdentifier="+team+"\n", "codesign", "-dv", bundle)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>com.apple.security.application-groups</key><array>`)
	for _, g := range groups {
		fmt.Fprintf(&b, "<string>%s</string>", g)
	}
	b.WriteString(`</array></dict></plist>`)
	f.runner.on(b.String(), "", "codesign", "-d", "--entitlements", ":-", bundle)
}

// appleSigned makes `codesign --verify --strict -R=anchor apple` pass.
func (f *fixture) appleSigned(bundle string) {
	f.runner.on("", "", "codesign", "--verify", "--strict", "-R=anchor apple", bundle)
}

// spotlight makes mdfind answer with hits for every query, plus the
// canary Finder that shows Spotlight is indexing.
func (f *fixture) spotlight(hits ...string) {
	f.spotlightRaw(append(hits, f.finder)...)
}

// spotlightRaw makes mdfind exit 0 with exactly hits, as it does with
// indexing turned off when hits is empty.
func (f *fixture) spotlightRaw(hits ...string) {
	out := strings.Join(hits, "\n")
	if out != "" {
		out += "\n"
	}
	f.runner.onPrefix(proc.Result{Stdout: []byte(out)}, "mdfind")
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
