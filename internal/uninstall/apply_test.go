package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// fooApp builds an app with a user-owned support dir, cache, prefs, user
// launch agent, a root-owned system support dir and a shared group container.
type fooApp struct {
	bundle, support, cache, prefs, agent, sysSupport, shared string
}

func (f *fixture) fooApp() fooApp {
	a := fooApp{bundle: f.app(f.apps, "Foo", "com.foo.app", "")}
	a.support = f.mkdir(f.userPath("Application Support", "com.foo.app"))
	f.write(filepath.Join(a.support, "data.db"), "data")
	a.cache = f.mkdir(f.userPath("Caches", "com.foo.app"))
	a.prefs = f.write(f.userPath("Preferences", "com.foo.app.plist"), "prefs")
	a.agent = f.launchd(f.userPath("LaunchAgents", "com.foo.app.agent.plist"), "com.foo.app.agent", a.bundle+"/Contents/MacOS/Foo", "")
	a.sysSupport = f.mkdir(filepath.Join(f.sysLib, "Application Support", "com.foo.app"))
	f.signing(a.bundle, "ABCDE12345")
	a.shared = f.mkdir(f.userPath("Group Containers", "ABCDE12345.suite"))
	return a
}

func (f *fixture) apply(p Plan, ao ApplyOptions) (Result, error) {
	// Round-trip through JSON as the CLI does.
	data, err := json.Marshal(p)
	if err != nil {
		f.t.Fatal(err)
	}
	var back Plan
	if err := json.Unmarshal(data, &back); err != nil {
		f.t.Fatal(err)
	}
	return Apply(context.Background(), f.opts, back, ao)
}

func movedPaths(r Result) map[string]string {
	out := map[string]string{}
	for _, m := range r.Moved {
		out[m.Path] = m.To
	}
	return out
}

func skippedReason(r Result, path string) string {
	for _, s := range r.Skipped {
		if s.Path == path {
			return s.Reason
		}
	}
	return ""
}

func TestApplyMovesPlannedItemsToTrashWithUniqueNames(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	f.write(filepath.Join(f.trash, "com.foo.app"), "already in trash")
	p := f.plan(a.bundle)
	// The user dropped the prefs from the plan: apply must leave them.
	var kept []Item
	for _, it := range p.Items {
		if it.Path != a.prefs {
			kept = append(kept, it)
		}
	}
	p.Items = kept

	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	moved := movedPaths(r)
	if filepath.Dir(r.TrashDir) != f.trash {
		t.Fatalf("trash dir = %q", r.TrashDir)
	}
	for _, src := range []string{a.bundle, a.support, a.cache, a.agent} {
		dest, ok := moved[src]
		if !ok || existsPath(src) || !existsPath(dest) || filepath.Dir(dest) != r.TrashDir {
			t.Fatalf("%s: moved=%v dest=%q", src, ok, dest)
		}
	}
	if r.Moved[0].Path != a.bundle {
		t.Fatalf("bundle not moved first: %+v", r.Moved)
	}
	if moved[a.support] == moved[a.cache] || filepath.Base(moved[a.support]) == "com.foo.app" && filepath.Base(moved[a.cache]) == "com.foo.app" {
		t.Fatalf("collision: %v", moved)
	}
	if got, _ := os.ReadFile(filepath.Join(f.trash, "com.foo.app")); string(got) != "already in trash" {
		t.Fatal("existing Trash entry was overwritten")
	}
	if !existsPath(filepath.Join(moved[a.support], "data.db")) {
		t.Fatal("support contents not moved with the directory")
	}
	for _, untouched := range []string{a.prefs, a.sysSupport, a.shared} {
		if !existsPath(untouched) {
			t.Fatalf("%s was touched", untouched)
		}
	}
	if len(r.NeedsRoot) != 1 || r.NeedsRoot[0].Path != a.sysSupport || !strings.HasPrefix(r.NeedsRoot[0].Command, "sudo mv -n -- ") {
		t.Fatalf("needs_root = %+v", r.NeedsRoot)
	}
	if skippedReason(r, a.shared) != "shared" {
		t.Fatalf("shared skip = %q", skippedReason(r, a.shared))
	}
	want := "launchctl bootout gui/" + strconv.FormatInt(f.opts.UID, 10) + "/com.foo.app.agent"
	if calls := f.runner.called("launchctl"); len(calls) != 1 || calls[0] != want {
		t.Fatalf("launchctl calls = %v, want %s", calls, want)
	}
}

func TestApplySkipsDriftedAndMissingItems(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	if err := os.RemoveAll(a.cache); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(a.support, "grew.bin"), strings.Repeat("z", 3<<20))
	if err := os.Chmod(a.prefs, 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := skippedReason(r, a.cache); got != "gone" {
		t.Fatalf("missing cache reason = %q", got)
	}
	if got := skippedReason(r, a.support); !strings.HasPrefix(got, "size changed") || !existsPath(a.support) {
		t.Fatalf("drifted support reason = %q", got)
	}
	if _, ok := movedPaths(r)[a.prefs]; !ok {
		t.Fatal("prefs with a mode change only should still move")
	}
}

func TestApplySkipsOwnerDrift(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.opts.FS = ownerFS{rootOwned: append(f.opts.FS.(ownerFS).rootOwned, a.prefs)}
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := skippedReason(r, a.prefs); !strings.HasPrefix(got, "owner changed") || !existsPath(a.prefs) {
		t.Fatalf("owner drift reason = %q", got)
	}
}

func TestApplyRefusesWhileRunning(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.procs = func() []process.Info {
		return []process.Info{{PID: 500, Command: "Foo", ExecutablePath: a.bundle + "/Contents/MacOS/Foo", StartTime: f.now}}
	}
	r, err := f.apply(p, ApplyOptions{})
	if !errors.Is(err, ErrProcessesRunning) {
		t.Fatalf("err = %v", err)
	}
	if len(r.Moved) != 0 || !existsPath(a.bundle) || len(f.runner.called("kill")) != 0 || len(f.runner.called("launchctl")) != 0 {
		t.Fatalf("refusal had side effects: %+v calls=%v", r, f.runner.calls)
	}
}

func TestApplyQuitTerminatesOnlyIdentityMatchedProcesses(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	exe := a.bundle + "/Contents/MacOS/Foo"
	start := f.now.Add(-time.Hour)
	calls := 0
	f.procs = func() []process.Info {
		calls++
		switch calls {
		case 1, 2: // apply's check, then the re-check before signalling 100
			return []process.Info{
				{PID: 100, Command: "Foo", ExecutablePath: exe, StartTime: start},
				{PID: 101, Command: "Foo Helper", ExecutablePath: exe, StartTime: start},
				{PID: 200, Command: "bash", ExecutablePath: "/bin/bash", StartTime: start},
			}
		case 3: // pid 101 was recycled by another process before its re-check
			return []process.Info{
				{PID: 100, Command: "Foo", ExecutablePath: exe, StartTime: start},
				{PID: 101, Command: "Foo Helper", ExecutablePath: exe, StartTime: start.Add(5 * time.Minute)},
			}
		default:
			return nil
		}
	}
	f.runner.on("", "", "kill", "-TERM", "100")
	r, err := f.apply(p, ApplyOptions{Quit: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if kills := f.runner.called("kill"); len(kills) != 1 || kills[0] != "kill -TERM 100" {
		t.Fatalf("kill calls = %v", kills)
	}
	if existsPath(a.bundle) || len(r.Moved) == 0 {
		t.Fatal("bundle not moved after the app quit")
	}
}

func TestApplyQuitAbortsWhenProcessesRemain(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	start := f.now.Add(-time.Hour)
	f.procs = func() []process.Info {
		return []process.Info{{PID: 100, Command: "Foo", ExecutablePath: a.bundle + "/Contents/MacOS/Foo", StartTime: start}}
	}
	f.runner.on("", "", "kill", "-TERM", "100")
	r, err := f.apply(p, ApplyOptions{Quit: true})
	if !errors.Is(err, ErrQuitTimeout) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.runner.called("kill") {
		if c != "kill -TERM 100" {
			t.Fatalf("unexpected signal %q", c)
		}
	}
	if len(r.Moved) != 0 || !existsPath(a.bundle) || !existsPath(a.support) {
		t.Fatal("items moved despite the abort")
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	if _, err := f.apply(p, ApplyOptions{}); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	trash, _ := os.ReadDir(f.trash)
	// The bundle is gone now, so a plain re-run is refused...
	if _, err := f.apply(p, ApplyOptions{}); !errors.Is(err, ErrBundleMissing) {
		t.Fatalf("second Apply err = %v, want ErrBundleMissing", err)
	}
	// ...and --leftovers finds nothing left to move.
	r, err := f.apply(p, ApplyOptions{Leftovers: true})
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if len(r.Moved) != 0 {
		t.Fatalf("second run moved %v", r.Moved)
	}
	for _, src := range []string{a.bundle, a.support, a.cache, a.prefs, a.agent} {
		if skippedReason(r, src) != "gone" {
			t.Fatalf("%s reason = %q", src, skippedReason(r, src))
		}
	}
	if again, _ := os.ReadDir(f.trash); len(again) != len(trash) {
		t.Fatal("second run changed the Trash")
	}
}

func TestApplyDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.procs = func() []process.Info {
		return []process.Info{{PID: 100, Command: "Foo", ExecutablePath: a.bundle + "/Contents/MacOS/Foo", StartTime: f.now}}
	}
	r, err := f.apply(p, ApplyOptions{DryRun: true, Quit: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !r.DryRun || len(r.Moved) == 0 || len(r.Terminated) != 1 || len(r.Bootouts) != 1 {
		t.Fatalf("dry run result = %+v", r)
	}
	for _, src := range []string{a.bundle, a.support, a.cache, a.prefs, a.agent} {
		if !existsPath(src) {
			t.Fatalf("dry run moved %s", src)
		}
	}
	if len(f.runner.called("kill")) != 0 || len(f.runner.called("launchctl")) != 0 {
		t.Fatalf("dry run ran %v", f.runner.calls)
	}
	if entries, _ := os.ReadDir(f.trash); len(entries) != 0 {
		t.Fatal("dry run wrote to the Trash")
	}
}

func TestApplyMovesSymlinkAsLink(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	targetDir := f.mkdir(filepath.Join(f.root, "elsewhere"))
	targetFile := f.write(filepath.Join(targetDir, "keep"), "keep me")
	link := f.userPath("Caches", "com.foo.app")
	f.mkdir(filepath.Dir(link))
	symlink(t, targetDir, link)
	p := f.plan(app)
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dest := movedPaths(r)[link]
	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("trash entry %s is not a symlink: %v", dest, err)
	}
	if got, _ := os.ReadFile(targetFile); string(got) != "keep me" || existsPath(link) {
		t.Fatal("symlink target was disturbed or the link stayed")
	}
}

func TestApplyLowConfidenceNeedsFlag(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	byName := f.mkdir(f.userPath("Application Support", "Foo"))
	p := f.plan(app)
	r, err := f.apply(p, ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(skippedReason(r, byName), "low confidence") {
		t.Fatalf("low confidence reason = %q", skippedReason(r, byName))
	}
	r, err = f.apply(p, ApplyOptions{IncludeLowConfidence: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := movedPaths(r)[byName]; !ok || existsPath(byName) {
		t.Fatal("low-confidence item not moved with the flag")
	}
}

func TestApplyRejectsTamperedPlan(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	other := f.app(f.apps, "Bar", "com.bar.app", "")
	otherSupport := f.mkdir(f.userPath("Application Support", "com.bar.app"))
	docs := f.mkdir(filepath.Join(f.home, "Documents"))

	for name, c := range map[string]struct {
		mutate func(*Plan)
		want   error
	}{
		"foreign item": {func(p *Plan) {
			p.Items = append(p.Items, Item{Path: otherSupport, Kind: KindSupport, Reason: ReasonBundleID, Confidence: ConfidenceHigh, Action: ActionTrash})
		}, ErrPlanRejected},
		"documents": {func(p *Plan) {
			p.Items = append(p.Items, Item{Path: docs, Kind: KindOther, Reason: ReasonBundleID, Confidence: ConfidenceHigh, Action: ActionTrash})
		}, ErrPlanRejected},
		"other app path": {func(p *Plan) { p.App.Path = other }, ErrBundleMissing},
		"widened helper": {func(p *Plan) { p.App.HelperIDs = append(p.App.HelperIDs, "com.bar.app") }, ErrPlanRejected},
		"other user":     {func(p *Plan) { p.UID++ }, ErrPlanRejected},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.plan(a.bundle)
			c.mutate(&p)
			r, err := f.apply(p, ApplyOptions{})
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(r.Moved) != 0 || !existsPath(a.bundle) || !existsPath(otherSupport) || !existsPath(docs) {
				t.Fatal("rejected plan had side effects")
			}
		})
	}
}

func TestApplyRefusesRoot(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.opts.UID = 0
	if _, err := f.apply(p, ApplyOptions{}); !errors.Is(err, ErrRunAsRoot) {
		t.Fatalf("err = %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/Library/It's Here"); got != `'/Library/It'\''s Here'` {
		t.Fatalf("quote = %s", got)
	}
}

func TestSameSizeClass(t *testing.T) {
	for _, c := range []struct {
		a, b uint64
		want bool
	}{{0, 4096, true}, {10 << 20, 12 << 20, true}, {10 << 20, 30 << 20, false}, {4096, 3 << 20, false}} {
		if got := sameSizeClass(c.a, c.b); got != c.want {
			t.Errorf("sameSizeClass(%d,%d) = %v", c.a, c.b, got)
		}
	}
}
