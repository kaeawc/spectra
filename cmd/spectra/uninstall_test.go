package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/uninstall"
)

// uninstallFixture builds a fake host tree; every path lives in t.TempDir().
func uninstallFixture(t *testing.T) (uninstall.Options, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	app := filepath.Join(root, "Applications", "Foo.app")
	support := filepath.Join(home, "Library", "Application Support", "com.foo.app")
	for _, d := range []string{filepath.Join(app, "Contents"), support, filepath.Join(home, ".Trash"), filepath.Join(root, "Library")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	info := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.foo.app</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	// Spotlight answers with only the canary Finder; every other command
	// fails.
	finder := filepath.Join(root, "System", "Library", "CoreServices", "Finder.app")
	if err := os.MkdirAll(filepath.Join(finder, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	finderInfo := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.apple.finder</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(finder, "Contents", "Info.plist"), []byte(finderInfo), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := proc.NewFake().
		On(func(c proc.Cmd) bool { return c.Name == "mdfind" }, proc.Response{Result: proc.Result{Stdout: []byte(finder + "\n")}}).
		On(func(proc.Cmd) bool { return true }, proc.Response{Result: proc.Result{ExitCode: 1}})
	opts := uninstall.Options{
		Home: home, AppRoots: []string{filepath.Join(root, "Applications")},
		SystemLibrary: filepath.Join(root, "Library"), Receipts: filepath.Join(root, "receipts"),
		SystemRoots: []string{filepath.Join(root, "System")}, SystemAppRoots: []string{filepath.Join(root, "System", "Applications")},
		UID: testUserUID, GIDs: []int64{testUserGID},
		Runner: fake, FS: testUserFS{},
		Procs: func(context.Context) []process.Info { return nil },
		Now:   func() time.Time { return time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC) },
		OS:    hostos.Darwin,
	}
	return opts, app, support
}

func TestUninstallPlanThenApply(t *testing.T) {
	opts, app, support := uninstallFixture(t)
	optsFn := func() uninstall.Options { return opts }
	var out, errOut bytes.Buffer
	if code := runUninstallWithIO([]string{"plan", app, "-json"}, &out, &errOut, optsFn); code != 0 {
		t.Fatalf("plan exit %d: %s", code, errOut.String())
	}
	var plan uninstall.Plan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("plan JSON: %v", err)
	}
	if plan.Schema != uninstall.PlanSchema || len(plan.Items) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	planFile := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planFile, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := runUninstallWithIO([]string{"apply", "--plan", planFile, "--dry-run"}, &out, &errOut, optsFn); code != 0 {
		t.Fatalf("dry run exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "dry run: nothing was changed") || !strings.Contains(out.String(), "would move "+app) {
		t.Fatalf("dry run output:\n%s", out.String())
	}
	if _, err := os.Stat(app); err != nil {
		t.Fatal("dry run moved the app")
	}

	out.Reset()
	if code := runUninstallWithIO([]string{"apply", "--plan", planFile, "-json"}, &out, &errOut, optsFn); code != 0 {
		t.Fatalf("apply exit %d: %s", code, errOut.String())
	}
	var res uninstall.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("apply JSON: %v", err)
	}
	if len(res.Moved) != 2 {
		t.Fatalf("moved = %+v", res.Moved)
	}
	for _, p := range []string{app, support} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s still present", p)
		}
	}

	// The bundle is gone: a re-run is refused unless --leftovers.
	out.Reset()
	errOut.Reset()
	if code := runUninstallWithIO([]string{"apply", "--plan", planFile}, &out, &errOut, optsFn); code != 1 || !strings.Contains(errOut.String(), "--leftovers") {
		t.Fatalf("re-run exit %d: %s", code, errOut.String())
	}
	out.Reset()
	if code := runUninstallWithIO([]string{"apply", "--plan", planFile, "--leftovers", "--data-only"}, &out, &errOut, optsFn); code != 0 || !strings.Contains(out.String(), "leftovers:") {
		t.Fatalf("--leftovers exit %d: %s%s", code, out.String(), errOut.String())
	}
}

func TestUninstallTextPlanAndUsage(t *testing.T) {
	opts, app, _ := uninstallFixture(t)
	optsFn := func() uninstall.Options { return opts }
	var out, errOut bytes.Buffer
	if code := runUninstallWithIO([]string{"plan", app}, &out, &errOut, optsFn); code != 0 {
		t.Fatalf("plan exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Moved to the Trash by apply: 2 items") || strings.Contains(out.String(), "warning") {
		t.Fatalf("text plan:\n%s", out.String())
	}
	// With Spotlight unavailable the text plan says so.
	noSpotlight := opts
	noSpotlight.Runner = proc.NewFake().On(func(proc.Cmd) bool { return true }, proc.Response{Result: proc.Result{ExitCode: 1}})
	out.Reset()
	if code := runUninstallWithIO([]string{"plan", app}, &out, &errOut, func() uninstall.Options { return noSpotlight }); code != 0 {
		t.Fatalf("plan exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "warning: Spotlight") {
		t.Fatalf("text plan without Spotlight:\n%s", out.String())
	}
	for _, args := range [][]string{{}, {"bogus"}, {"plan"}, {"apply"}, {"apply", "x", "--plan", "p"}} {
		if code := runUninstallWithIO(args, &out, &errOut, optsFn); code != 2 {
			t.Errorf("%v exit %d, want 2", args, code)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallWithIO([]string{"apply", "--plan", bad}, &out, &errOut, optsFn); code != 1 {
		t.Errorf("bad plan exit %d, want 1", code)
	}
}

func TestUninstallRegistered(t *testing.T) {
	for _, sc := range subcommandList() {
		if sc.name == "uninstall" {
			return
		}
	}
	t.Fatal("uninstall not registered")
}

// bootoutFails answers launchctl with a failure that is not "not loaded"
// and passes every other command to next.
type bootoutFails struct{ next proc.Runner }

func (b bootoutFails) Run(ctx context.Context, c proc.Cmd) (proc.Result, error) {
	if c.Name == "launchctl" {
		return proc.Result{ExitCode: 5, Stderr: []byte("Boot-out failed: 5: Input/output error")}, nil
	}
	return b.next.Run(ctx, c)
}

func TestUninstallApplyExits3WhenBootoutFails(t *testing.T) {
	opts, app, _ := uninstallFixture(t)
	agent := filepath.Join(opts.Home, "Library", "LaunchAgents", "com.foo.app.plist")
	if err := os.MkdirAll(filepath.Dir(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>com.foo.app</string><key>Program</key><string>` +
		filepath.Join(app, "Contents", "MacOS", "Foo") + `</string></dict></plist>`
	if err := os.WriteFile(agent, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	opts.Runner = bootoutFails{next: opts.Runner}
	optsFn := func() uninstall.Options { return opts }
	var out, errOut bytes.Buffer
	if code := runUninstallWithIO([]string{"plan", app, "-json"}, &out, &errOut, optsFn); code != 0 {
		t.Fatalf("plan exit %d: %s", code, errOut.String())
	}
	planFile := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(planFile, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runUninstallWithIO([]string{"apply", "--plan", planFile}, &out, &errOut, optsFn); code != 3 {
		t.Fatalf("apply exit %d, want 3: %s%s", code, out.String(), errOut.String())
	}
	if _, err := os.Lstat(agent); err != nil {
		t.Fatal("agent plist moved after a failed bootout")
	}
	if !strings.Contains(out.String(), "bootout failed") {
		t.Fatalf("output does not report the failed bootout:\n%s", out.String())
	}
}

// testUserUID and testUserGID are the user the uninstall fixture acts as:
// the real one, or 501/20 when the tests run as root, which apply refuses.
var testUserUID, testUserGID = func() (int64, int64) {
	if os.Getuid() == 0 {
		return 501, 20
	}
	return int64(os.Getuid()), int64(os.Getgid())
}()

// testUserFS presents the files the test process created as the test
// user's.
type testUserFS struct{ uninstall.OSFS }

func (f testUserFS) Meta(path string) (uninstall.Meta, error) {
	m, err := f.OSFS.Meta(path)
	if err == nil && m.UID == int64(os.Getuid()) {
		m.UID, m.GID = testUserUID, testUserGID
	}
	return m, err
}
