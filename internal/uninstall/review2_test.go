package uninstall

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/proc"
)

// Ported from the second review: a vendor-prefix main id (com.google) with
// a sub-bundle claiming another installed app's id moved that app's data.
func TestPlanHelperIDOfAnotherInstalledAppIsShared(t *testing.T) {
	f := newFixture(t)
	f.app(f.apps, "Browser", "com.google.Chrome", "")
	caches := f.mkdir(f.userPath("Caches", "com.google.Chrome"))
	prefs := f.write(f.userPath("Preferences", "com.google.Chrome.plist"), "x")
	support := f.mkdir(f.userPath("Application Support", "com.google.Chrome"))
	evil := f.app(filepath.Join(f.home, "Downloads"), "Evil", "com.google", "")
	f.helper(evil, "PlugIns", "X.appex", "com.google.Chrome")
	p := f.plan(evil)
	for _, path := range []string{caches, prefs, support} {
		if it, ok := itemAt(p, path); !ok || it.Action != ActionReportOnly || !it.Shared {
			t.Fatalf("%s = %+v, want shared report-only", path, it)
		}
	}
	if _, err := f.apply(p, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, path := range []string{caches, prefs, support} {
		if !existsPath(path) {
			t.Fatalf("%s was moved", path)
		}
	}
}

// Another installed app whose id sits under this app's id makes every
// id-derived item shared, even <location>/<main id>.
func TestPlanOtherAppUnderMainIDMakesIDItemsShared(t *testing.T) {
	f := newFixture(t)
	f.app(f.apps, "Viewer", "com.vendor.suite.viewer", "")
	app := f.app(f.apps, "Suite", "com.vendor.suite", "")
	own := f.mkdir(f.userPath("Caches", "com.vendor.suite"))
	viewer := f.mkdir(f.userPath("Caches", "com.vendor.suite.viewer"))
	f.helper(app, "PlugIns", "V.appex", "com.vendor.suite.viewer")
	p := f.plan(app)
	f.mustItem(p, app, KindBundle, ReasonBundleID, ActionTrash)
	for _, path := range []string{own, viewer} {
		it, ok := itemAt(p, path)
		if !ok || it.Action != ActionReportOnly || !strings.Contains(it.Note, "com.vendor.suite.viewer") {
			t.Fatalf("%s = %+v, want report-only naming the other app's id", path, it)
		}
	}
}

// A two-segment id is auto-trashed only with a verified team id.
func TestPlanShortBundleIDNeedsVerifiedTeam(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Short", "com.short", "")
	c := f.mkdir(f.userPath("Caches", "com.short"))
	p := f.plan(app)
	f.mustItem(p, app, KindBundle, ReasonBundleID, ActionTrash)
	if it := f.mustItem(p, c, KindCache, ReasonBundleID, ActionReportOnly); !strings.Contains(it.Note, "three segments") {
		t.Fatalf("note = %q", it.Note)
	}
	f.signing(app, "ABCDE12345")
	f.mustItem(f.plan(app), c, KindCache, ReasonBundleID, ActionTrash)
}

// Ported from the second review: com.apple main id with helper
// com.apple.Safari moved Safari's data.
func TestPlanApplePrefixHelperIsReportOnly(t *testing.T) {
	f := newFixture(t)
	caches := f.mkdir(f.userPath("Caches", "com.apple.Safari"))
	containers := f.mkdir(f.userPath("Containers", "com.apple.Safari"))
	evil := f.app(f.apps, "Evil", "com.apple", "")
	f.helper(evil, "PlugIns", "X.appex", "com.apple.Safari")
	f.signing(evil, "ABCDE12345")
	p := f.plan(evil)
	for _, path := range []string{caches, containers} {
		if it, ok := itemAt(p, path); !ok || it.Action != ActionReportOnly {
			t.Fatalf("%s = %+v, want report-only", path, it)
		}
	}
	if _, err := f.apply(p, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !existsPath(caches) || !existsPath(containers) {
		t.Fatal("Safari data moved")
	}
}

// Ported from the second review: a third-party app claiming com.apple.Safari
// in normal mode. Its data is report-only unless the bundle is Apple-signed.
func TestPlanAppleIDNeedsAppleSignature(t *testing.T) {
	f := newFixture(t)
	c := f.mkdir(f.userPath("Caches", "com.apple.Safari"))
	group := f.mkdir(f.userPath("Group Containers", "group.com.apple.Safari"))
	evil := f.app(f.apps, "Evil", "com.apple.Safari", "")
	f.signing(evil, "", "group.com.apple.Safari")
	p := f.plan(evil)
	f.mustItem(p, evil, KindBundle, ReasonBundleID, ActionTrash)
	if it := f.mustItem(p, c, KindCache, ReasonBundleID, ActionReportOnly); !strings.Contains(it.Note, "Apple") {
		t.Fatalf("note = %q", it.Note)
	}
	f.mustItem(p, group, KindGroupContainer, ReasonEntitlementGroup, ActionReportOnly)
	if p.App.AppleSigned {
		t.Fatal("unsigned app recorded as Apple-signed")
	}
	if _, err := f.apply(p, ApplyOptions{}); err != nil || !existsPath(c) {
		t.Fatalf("Apply: err=%v cache exists=%v", err, existsPath(c))
	}

	// A real Apple app outside /System may proceed.
	f2 := newFixture(t)
	c2 := f2.mkdir(f2.userPath("Caches", "com.apple.iWork.Keynote"))
	real := f2.app(f2.apps, "Keynote", "com.apple.iWork.Keynote", "")
	f2.appleSigned(real)
	p2 := f2.plan(real)
	if !p2.App.AppleSigned {
		t.Fatal("Apple-signed app not recorded")
	}
	f2.mustItem(p2, c2, KindCache, ReasonBundleID, ActionTrash)
}

// A top-level symlinked .app (Safari in /Applications points into the
// system volume) counts as installed for the duplicate check.
func TestPlanSymlinkedInstalledAppCountsAsDuplicate(t *testing.T) {
	f := newFixture(t)
	real := f.app(filepath.Join(f.root, "System", "Cryptexes", "App"), "Safari", "com.apple.Safari", "")
	symlink(t, real, filepath.Join(f.apps, "Safari.app"))
	c := f.mkdir(f.userPath("Caches", "com.apple.Safari"))
	evil := f.app(filepath.Join(f.home, "Downloads"), "Evil", "com.apple.Safari", "")
	f.appleSigned(evil)
	it := f.mustItem(f.plan(evil), c, KindCache, ReasonBundleID, ActionReportOnly)
	if !strings.Contains(it.Note, "same bundle id") {
		t.Fatalf("note = %q", it.Note)
	}
}

// Ported from the second review: a team id codesign -dv reports but that
// fails `codesign --verify` claimed another vendor's team group.
func TestPlanUnverifiedTeamIDIsNotTrusted(t *testing.T) {
	f := newFixture(t)
	g := f.mkdir(f.userPath("Group Containers", "ABCDE12345.com.vendor.shared"))
	evil := f.app(f.apps, "Evil", "com.evil.app", "")
	f.signingUnverified(evil, "ABCDE12345", "ABCDE12345.com.vendor.shared")
	p := f.plan(evil)
	if p.App.TeamID != "" {
		t.Fatalf("team id = %q, want empty for an unverified signature", p.App.TeamID)
	}
	if it := f.mustItem(p, g, KindGroupContainer, ReasonEntitlementGroup, ActionReportOnly); !it.Shared {
		t.Fatal("group of an unverified team must be shared")
	}
	if _, err := f.apply(p, ApplyOptions{}); err != nil || !existsPath(g) {
		t.Fatalf("Apply: err=%v group exists=%v", err, existsPath(g))
	}
}

// readTeamID against the real codesign: a modified copy of a signed bundle
// fails verification and yields no team id. Read-only on the original.
func TestReadTeamIDRealCodesignRejectsModifiedBundle(t *testing.T) {
	if runtime.GOOS != "darwin" || testing.Short() {
		t.Skip("needs macOS codesign")
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign not found")
	}
	ctx := context.Background()
	r := proc.OS{}
	src := smallTeamSignedApp(ctx, t, r)
	if src == "" {
		t.Skip("no small team-signed app in /Applications to copy")
	}
	dst := filepath.Join(t.TempDir(), "Copy.app")
	if out, err := exec.Command("cp", "-R", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	if readTeamID(ctx, r, dst) == "" {
		t.Skip("copy does not verify before modification")
	}
	plist := filepath.Join(dst, "Contents", "Info.plist")
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, append(data, []byte("\n<!-- modified -->\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if team := readTeamID(ctx, r, dst); team != "" {
		t.Fatalf("modified bundle reported team %q", team)
	}
}

// smallTeamSignedApp finds a top-level /Applications bundle under 40 MiB
// whose signature verifies with a team id, or "".
func smallTeamSignedApp(ctx context.Context, t *testing.T, r proc.Runner) string {
	t.Helper()
	entries, err := os.ReadDir("/Applications")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		p := filepath.Join("/Applications", e.Name())
		if !strings.HasSuffix(e.Name(), ".app") || !e.IsDir() || !smallerThan(p, 40<<20) {
			continue
		}
		if readTeamID(ctx, r, p) != "" {
			return p
		}
	}
	return ""
}

func smallerThan(dir string, limit int64) bool {
	var total int64
	errTooBig := errors.New("too big")
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		if total > limit {
			return errTooBig
		}
		return nil
	})
	return err == nil
}

// Ported from the second review: an installed copy in a vendor subfolder
// of an app root was not seen, so --leftovers moved its data.
func TestLeftoversSeesAppsInVendorSubfolder(t *testing.T) {
	f := newFixture(t)
	f.app(filepath.Join(f.apps, "Vendor Suite"), "Foo", "com.foo.app", "")
	data := f.mkdir(f.userPath("Application Support", "com.foo.app"))
	cp := f.app(filepath.Join(f.home, "Downloads"), "Foo", "com.foo.app", "")
	p := f.plan(cp)
	if it := f.mustItem(p, data, KindSupport, ReasonBundleID, ActionReportOnly); !strings.Contains(it.Note, "Vendor Suite") {
		t.Fatalf("note = %q", it.Note)
	}
	if err := os.RemoveAll(cp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.apply(p, ApplyOptions{Leftovers: true}); !errors.Is(err, ErrPlanRejected) {
		t.Fatalf("err = %v, want rejection", err)
	}
	if !existsPath(data) {
		t.Fatal("installed app's data moved")
	}
}

// A Spotlight hit outside the planned bundle (and the Trash) is installed.
func TestSpotlightHitIsInstalled(t *testing.T) {
	f := newFixture(t)
	elsewhere := f.app(filepath.Join(f.root, "Volumes", "Data", "Apps"), "Foo", "com.foo.app", "")
	data := f.mkdir(f.userPath("Application Support", "com.foo.app"))
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.helper(app, "PlugIns", "X.appex", "com.foo.app.ext")
	trashed := f.app(f.trash, "Old Foo", "com.foo.app", "")
	f.spotlight(app, filepath.Join(app, "Contents", "PlugIns", "X.appex"), trashed, elsewhere)
	p := f.plan(app)
	if it := f.mustItem(p, data, KindSupport, ReasonBundleID, ActionReportOnly); !strings.Contains(it.Note, elsewhere) {
		t.Fatalf("note = %q", it.Note)
	}
	calls := f.runner.called("mdfind")
	if len(calls) == 0 || !strings.Contains(calls[0], "kMDItemCFBundleIdentifier == 'com.foo.app'cd") {
		t.Fatalf("mdfind calls = %v", calls)
	}

	// Only the planned bundle and a copy in the Trash: not shared.
	f.spotlight(app, trashed)
	f.mustItem(f.plan(app), data, KindSupport, ReasonBundleID, ActionTrash)

	// --leftovers refuses on a hit.
	f.spotlight(elsewhere)
	p.App.Path = filepath.Join(f.apps, "Gone.app")
	p.Items = []Item{{Path: data, Kind: KindSupport, Reason: ReasonBundleID, Confidence: ConfidenceHigh, Action: ActionTrash}}
	if _, err := f.apply(p, ApplyOptions{Leftovers: true}); !errors.Is(err, ErrPlanRejected) || !existsPath(data) {
		t.Fatalf("leftovers err = %v", err)
	}
}

// Spotlight failing makes --leftovers refuse; normal mode still plans but
// says so.
func TestSpotlightUnavailable(t *testing.T) {
	f := newFixture(t)
	f.runner.onPrefix(proc.Result{ExitCode: 1}, "mdfind")
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	data := f.mkdir(f.userPath("Application Support", "com.foo.app"))
	p := f.plan(app)
	f.mustItem(p, data, KindSupport, ReasonBundleID, ActionTrash)
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "Spotlight") {
		t.Fatalf("warnings = %v", p.Warnings)
	}
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	if _, err := f.apply(p, ApplyOptions{Leftovers: true}); !errors.Is(err, ErrPlanRejected) || !strings.Contains(err.Error(), "Spotlight") {
		t.Fatalf("leftovers err = %v, want Spotlight refusal", err)
	}
	if !existsPath(data) {
		t.Fatal("data moved without Spotlight")
	}
}

// Ported from the second review: a job that runs from the bundle but has a
// foreign label was booted out by that label, and a com.apple label was
// printed in a bootout command.
func TestApplyNeverBootsOutForeignOrAppleLabels(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Evil", "com.evil.app", "")
	agent := f.launchd(f.userPath("LaunchAgents", "evil.plist"), "com.google.keystone.agent", filepath.Join(app, "Contents/MacOS/Evil"), "")
	daemon := f.launchd(filepath.Join(f.sysLib, "LaunchDaemons", "evil.plist"), "com.apple.mDNSResponder", filepath.Join(app, "Contents/MacOS/Evil"), "")
	p := f.plan(app)
	f.mustItem(p, agent, KindLaunchAgent, ReasonLaunchdProgram, ActionTrash)
	if it := f.mustItem(p, daemon, KindLaunchDaemon, ReasonLaunchdProgram, ActionNeedsRoot); strings.Contains(it.Command, "bootout") {
		t.Fatalf("plan command = %q", it.Command)
	}
	f.runner.on("", "", "launchctl", "bootout", "gui/"+itoa(f.opts.UID)+"/com.google.keystone.agent")
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.runner.called("launchctl")) != 0 || len(r.Bootouts) != 0 {
		t.Fatalf("bootouts = %v", r.Bootouts)
	}
	if _, ok := movedPaths(r)[agent]; !ok {
		t.Fatalf("agent plist not moved: %v", r.Skipped)
	}
	for _, n := range r.NeedsRoot {
		if strings.Contains(n.Command, "bootout") {
			t.Fatalf("needs-root command = %q", n.Command)
		}
	}
}

// orderFS records whether launchctl had run when the bundle was renamed.
type orderFS struct {
	renameFailFS
	runner        *fakeRunner
	bundle        string
	bootoutBefore *bool
}

func (f orderFS) Rename(oldpath, newpath string) error {
	if oldpath == f.bundle && len(f.runner.called("launchctl")) > 0 {
		*f.bootoutBefore = true
	}
	return f.renameFailFS.Rename(oldpath, newpath)
}

// Ported from the second review: agents were booted out, then the bundle
// move failed, leaving the app installed with its agents unloaded.
func TestApplyMovesBundleBeforeBootout(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	base := f.opts.FS.(ownerFS)
	before := false
	f.opts.FS = orderFS{renameFailFS: renameFailFS{ownerFS: base, fail: a.bundle}, runner: f.runner, bundle: a.bundle, bootoutBefore: &before}
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.runner.called("launchctl")) != 0 || len(r.Bootouts) != 0 || len(r.Moved) != 0 || !existsPath(a.agent) {
		t.Fatalf("failed bundle move still booted out or moved: bootouts=%v moved=%v", r.Bootouts, r.Moved)
	}

	f.opts.FS = orderFS{renameFailFS: renameFailFS{ownerFS: base}, runner: f.runner, bundle: a.bundle, bootoutBefore: &before}
	r, err = f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if before || len(r.Bootouts) != 1 || r.Moved[0].Path != a.bundle || existsPath(a.agent) {
		t.Fatalf("bootoutBefore=%v bootouts=%v moved=%v", before, r.Bootouts, r.Moved)
	}
}

// Ported from the second review: a plan listing the same path twice.
func TestApplyRejectsDuplicatePlanPaths(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	c := f.mkdir(f.userPath("Caches", "com.foo.app"))
	p := f.plan(app)
	dupBundle := p
	dupBundle.Items = append([]Item{p.Items[0]}, p.Items...)
	cache, _ := itemAt(p, c)
	other := cache
	other.Reason = ReasonHelperOfBundle
	other.Path = filepath.Join(filepath.Dir(c), "COM.FOO.APP") // same folder on a case-insensitive volume
	dupReason := p
	dupReason.Items = append(append([]Item{}, p.Items...), other)
	for name, plan := range map[string]Plan{"same item": dupBundle, "other reason": dupReason} {
		r, err := f.apply(plan, ApplyOptions{})
		if !errors.Is(err, ErrPlanRejected) || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if len(r.Moved) != 0 || !existsPath(app) {
			t.Fatalf("%s: moved %v", name, r.Moved)
		}
	}
}
