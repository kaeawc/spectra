package uninstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/proc"
)

// archiveCopy plans an Apple-signed copy of Archive Utility in ~/Downloads
// that has a container and preferences.
func (f *fixture) archiveCopy() (bundle, prefs, container string) {
	bundle = f.app(filepath.Join(f.home, "Downloads"), "Archive Utility", "com.apple.archiveutility", "")
	f.appleSigned(bundle)
	prefs = f.write(f.userPath("Preferences", "com.apple.archiveutility.plist"), "x")
	container = f.mkdir(f.userPath("Containers", "com.apple.archiveutility"))
	return bundle, prefs, container
}

func (f *fixture) mustReportOnly(p Plan, want ...string) {
	f.t.Helper()
	for _, path := range want {
		it, ok := itemAt(p, path)
		if !ok {
			f.t.Fatalf("plan missing %s; items: %v", path, paths(p))
		}
		if it.Action != ActionReportOnly || !it.Shared {
			f.t.Fatalf("%s = %s shared=%v, want report_only shared", path, it.Action, it.Shared)
		}
	}
}

// Ported from the third review: an Apple-signed copy of a CoreServices app,
// with Spotlight down, moved the real system app's data, because the system
// copy lives in /System/Library/CoreServices/Applications.
func TestPlanSeesCoreServicesCopyWithoutSpotlight(t *testing.T) {
	f := newFixture(t)
	f.runner.onPrefix(proc.Result{ExitCode: 1}, "mdfind")
	f.app(filepath.Join(f.coreServices, "Applications"), "Archive Utility", "com.apple.archiveutility", "")
	bundle, prefs, container := f.archiveCopy()
	p := f.plan(bundle)
	f.mustReportOnly(p, prefs, container)
	if it, _ := itemAt(p, prefs); !strings.Contains(it.Note, "same bundle id") {
		t.Fatalf("note = %q, want the CoreServices copy named", it.Note)
	}
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !existsPath(prefs) || !existsPath(container) {
		t.Fatalf("system app data moved: %v", r.Moved)
	}
}

func TestDefaultSystemAppRootsIncludeCoreServices(t *testing.T) {
	o := Options{Home: "/Users/x"}.withDefaults()
	for _, want := range []string{"/System/Applications", "/System/Library/CoreServices", "/System/Library/CoreServices/Applications"} {
		if !containsFold(o.SystemAppRoots, want) {
			t.Fatalf("SystemAppRoots = %v, missing %s", o.SystemAppRoots, want)
		}
	}
}

// With Spotlight down, a com.apple.* id is report-only even when the bundle
// is Apple-signed and no other copy was seen in the scanned folders.
func TestPlanAppleIDsReportOnlyWhenSpotlightDown(t *testing.T) {
	f := newFixture(t)
	f.runner.onPrefix(proc.Result{ExitCode: 1}, "mdfind")
	bundle, prefs, container := f.archiveCopy()
	p := f.plan(bundle)
	f.mustReportOnly(p, prefs, container)
	f.mustItem(p, bundle, KindBundle, ReasonBundleID, ActionTrash)
	if len(p.Warnings) == 0 {
		t.Fatal("no Spotlight warning")
	}
}

// mdfind with indexing off exits 0 with no hits; without the canary that
// is not an answer.
func TestSpotlightWithoutCanaryDidNotAnswer(t *testing.T) {
	f := newFixture(t)
	f.spotlightRaw()
	bundle, prefs, container := f.archiveCopy()
	p := f.plan(bundle)
	f.mustReportOnly(p, prefs, container)
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "Spotlight") {
		t.Fatalf("warnings = %v", p.Warnings)
	}
	if calls := f.runner.called("mdfind"); len(calls) != 1 || !strings.Contains(calls[0], "'com.apple.finder'cd") {
		t.Fatalf("mdfind calls = %v, want the canary asked", calls)
	}

	// A healthy Spotlight (canary present) lets an Apple-signed app's own
	// data be moved.
	g := newFixture(t)
	bundle, prefs, _ = g.archiveCopy()
	g.mustItem(g.plan(bundle), prefs, KindPreferences, ReasonBundleID, ActionTrash)
}

func TestLeftoversRefusesWithoutCanary(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	if err := os.RemoveAll(a.bundle); err != nil {
		t.Fatal(err)
	}
	f.spotlightRaw()
	if _, err := f.apply(p, ApplyOptions{Leftovers: true}); !errors.Is(err, ErrPlanRejected) || !strings.Contains(err.Error(), "Spotlight") {
		t.Fatalf("Apply = %v, want Spotlight refusal", err)
	}
	if !existsPath(a.cache) {
		t.Fatal("cache moved")
	}
}

// Ported from the third review: ~/Library/Caches as a symlink to a folder
// outside the home had the folder's contents moved.
func TestSymlinkedLocationFolderRefused(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	outside := f.mkdir(filepath.Join(f.root, "elsewhere"))
	victim := f.mkdir(filepath.Join(outside, "com.foo.app"))
	if err := os.Symlink(outside, f.userPath("Caches")); err != nil {
		t.Fatal(err)
	}
	p := f.plan(app)
	f.mustNotItem(p, f.userPath("Caches", "com.foo.app"))
	if !strings.Contains(strings.Join(p.Warnings, "\n"), "symbolic link") {
		t.Fatalf("warnings = %v", p.Warnings)
	}
	if _, err := f.apply(p, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !existsPath(victim) {
		t.Fatal("folder outside the home moved")
	}
}

// A plan built before ~/Library/Caches became a symlink is rejected.
func TestApplyRejectsLocationSymlinkedAfterPlan(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.mkdir(f.userPath("Caches", "com.foo.app"))
	p := f.plan(app)
	outside := f.mkdir(filepath.Join(f.root, "elsewhere"))
	victim := f.mkdir(filepath.Join(outside, "com.foo.app"))
	if err := os.RemoveAll(f.userPath("Caches")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.userPath("Caches")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.apply(p, ApplyOptions{}); !errors.Is(err, ErrPlanRejected) {
		t.Fatalf("Apply = %v, want ErrPlanRejected", err)
	}
	if !existsPath(victim) || !existsPath(app) {
		t.Fatal("something moved")
	}
}

// An item whose parent resolves elsewhere through a symlink higher up
// (~/Library itself) is refused at plan and at apply.
func TestResolvedParentMustMatchTextualParent(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	cache := f.mkdir(f.userPath("Caches", "com.foo.app"))
	p := f.plan(app)
	if _, ok := itemAt(p, cache); !ok {
		t.Fatal("cache not planned")
	}
	real := filepath.Join(f.home, "LibraryReal")
	if err := os.Rename(f.lib, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, f.lib); err != nil {
		t.Fatal(err)
	}
	f.mustNotItem(f.plan(app), cache)
	if _, err := f.apply(p, ApplyOptions{}); !errors.Is(err, ErrPlanRejected) {
		t.Fatalf("Apply = %v, want ErrPlanRejected", err)
	}
	if !existsPath(filepath.Join(real, "Caches", "com.foo.app")) || !existsPath(app) {
		t.Fatal("something moved")
	}
}

// Ported from the third review: a bootout that failed was followed by
// moving the agent's plist, leaving a loaded job with no plist.
func TestFailedBootoutKeepsPlist(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.runner.onPrefix(proc.Result{ExitCode: 5, Stderr: []byte("Boot-out failed: 5: Input/output error")}, "launchctl", "bootout")
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !existsPath(a.agent) {
		t.Fatal("plist moved after a failed bootout")
	}
	if why := skippedReason(r, a.agent); !strings.Contains(why, "bootout failed") {
		t.Fatalf("skip reason = %q", why)
	}
	if len(r.Bootouts) != 1 || !r.Bootouts[0].Failed {
		t.Fatalf("bootouts = %+v", r.Bootouts)
	}
	if existsPath(a.cache) {
		t.Fatal("other items should still move")
	}
}

func TestNotLoadedBootoutStillMovesPlist(t *testing.T) {
	for _, msg := range []string{"Boot-out failed: 3: No such process", "Could not find specified service", "service not loaded"} {
		f := newFixture(t)
		a := f.fooApp()
		p := f.plan(a.bundle)
		f.runner.onPrefix(proc.Result{ExitCode: 3, Stderr: []byte(msg)}, "launchctl", "bootout")
		r, err := f.apply(p, ApplyOptions{})
		if err != nil {
			t.Fatalf("%s: Apply: %v", msg, err)
		}
		if existsPath(a.agent) || len(r.Bootouts) != 1 || r.Bootouts[0].Failed {
			t.Fatalf("%s: plist kept or bootout failed: %+v", msg, r.Bootouts)
		}
	}
}

// Ported from the third review: a symlink in an app root pointing at the
// planned bundle made the app look installed twice.
func TestSymlinkToPlannedBundleIsSameApp(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	ua := f.mkdir(filepath.Join(f.home, "Applications"))
	f.opts.AppRoots = []string{f.apps, ua}
	if err := os.Symlink(app, filepath.Join(ua, "Foo.app")); err != nil {
		t.Fatal(err)
	}
	cache := f.mkdir(f.userPath("Caches", "com.foo.app"))
	f.mustItem(f.plan(app), cache, KindCache, ReasonBundleID, ActionTrash)
}

// Ported from the third review: a Spotlight hit equal to the planned bundle
// ignoring case made the app look installed twice.
func TestSpotlightHitOfSelfIgnoringCase(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.spotlight(strings.ToUpper(app[:2])+app[2:], strings.ToUpper(app))
	cache := f.mkdir(f.userPath("Caches", "com.foo.app"))
	f.mustItem(f.plan(app), cache, KindCache, ReasonBundleID, ActionTrash)
}

// A real second copy found by Spotlight still makes the data shared.
func TestSpotlightSecondCopyStillShared(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	other := f.app(filepath.Join(f.home, "Downloads"), "Foo", "com.foo.app", "")
	f.spotlight(other)
	cache := f.mkdir(f.userPath("Caches", "com.foo.app"))
	f.mustReportOnly(f.plan(app), cache)
}
