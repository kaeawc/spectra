package uninstall

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func TestValidIDRejectsPathComponents(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "com..x", ".com.x", "com.x.", "single", "com.x/..", "com.x\x00y", "com." + strings.Repeat("x", 250)} {
		if validID(bad) {
			t.Errorf("validID(%q) = true", bad)
		}
	}
	for _, good := range []string{"com.foo.app", "ABCDE12345.suite", "group.com.foo.app", "us.vendor.pkg.meeting", "com.Foo_bar-2.x"} {
		if !validID(good) {
			t.Errorf("validID(%q) = false", good)
		}
	}
}

// Ported from the review probe: an Info.plist bundle id of ".." or "."
// made filepath.Join climb to ~/Library, which apply then trashed.
func TestPlanRejectsUnsafeBundleIDs(t *testing.T) {
	for _, id := range []string{"..", ".", "a/b", "com..x"} {
		t.Run(id, func(t *testing.T) {
			f := newFixture(t)
			f.mkdir(f.userPath("Caches", "com.other.cache"))
			f.write(f.userPath("Application Support", "Other", "x"), "data")
			app := f.app(f.apps, "Evil", id, "")
			if p, err := BuildPlan(context.Background(), f.opts, app); err == nil {
				t.Fatalf("BuildPlan accepted id %q: %v", id, paths(p))
			}
			if !existsPath(f.userPath("Caches", "com.other.cache")) {
				t.Fatal("library disturbed")
			}
		})
	}
}

// A hand-edited plan whose bundle is gone and whose ids are path tricks is
// rejected before anything is derived from them, with or without --leftovers.
func TestApplyRejectsUnsafeIDsInEditedPlan(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.mkdir(f.userPath("Caches", "com.other.cache"))
	sysSupport := f.mkdir(filepath.Join(f.sysLib, "Application Support", "com.other"))
	base := f.plan(app)
	edits := map[string]func(*Plan){}
	for _, bad := range []string{"..", ".", "a/b", "com..x"} {
		bad := bad
		edits["bundle "+bad] = func(p *Plan) { p.App.BundleID = bad }
		edits["label "+bad] = func(p *Plan) { p.App.DeclaredJobs = []string{bad} }
		edits["group "+bad] = func(p *Plan) { p.App.AppGroups = []string{bad} }
		edits["receipt "+bad] = func(p *Plan) { p.App.Receipts = []string{bad} }
	}
	for name, edit := range edits {
		for _, leftovers := range []bool{false, true} {
			p := base
			p.App.Path = filepath.Join(f.apps, "Gone.app")
			p.Items = []Item{{Path: f.lib, Kind: KindSupport, Reason: ReasonBundleID, Confidence: ConfidenceHigh, Action: ActionTrash}}
			edit(&p)
			r, err := f.apply(p, ApplyOptions{Leftovers: leftovers})
			if !errors.Is(err, ErrPlanRejected) {
				t.Errorf("%s leftovers=%v: err = %v, want ErrPlanRejected", name, leftovers, err)
			}
			if len(r.Moved) != 0 || !existsPath(f.lib) || !existsPath(sysSupport) {
				t.Fatalf("%s: rejected plan had side effects: %+v", name, r.Moved)
			}
		}
	}
}

func TestCheckItemPath(t *testing.T) {
	f := newFixture(t)
	o := f.opts.withDefaults()
	app := filepath.Join(f.apps, "Foo.app")
	ok := []Item{
		{Path: app, Kind: KindBundle},
		{Path: f.userPath("Caches", "com.foo.app"), Kind: KindCache},
		{Path: f.userPath("Preferences", "ByHost", "com.foo.app.0123456789AB.plist"), Kind: KindPreferences},
		{Path: filepath.Join(f.sysLib, "LaunchDaemons", "com.foo.app.plist"), Kind: KindLaunchDaemon},
		{Path: "/usr/local/bin/footool", Kind: KindReceiptFile},
	}
	for _, it := range ok {
		if err := checkItemPath(o, app, it); err != nil {
			t.Errorf("%s: %v", it.Path, err)
		}
	}
	bad := []Item{
		{Path: f.userPath("Caches"), Kind: KindCache},
		{Path: f.lib, Kind: KindSupport},
		{Path: f.home, Kind: KindOther},
		{Path: f.sysLib, Kind: KindSupport},
		{Path: f.apps, Kind: KindBundle},
		{Path: "/", Kind: KindReceiptFile},
		{Path: f.userPath("Caches", "x", "com.foo.app"), Kind: KindCache},
		{Path: f.userPath("Caches", "com.foo.app"), Kind: KindGroupContainer},
		{Path: f.userPath("Caches") + "/../Caches/com.foo.app", Kind: KindCache},
		{Path: filepath.Join(f.apps, "Other.app"), Kind: KindBundle},
	}
	for _, it := range bad {
		if err := checkItemPath(o, app, it); err == nil {
			t.Errorf("%s (%s) passed the guard", it.Path, it.Kind)
		}
	}
}

// The guard runs on a hand-edited plan at apply time, before matching.
func TestApplyStructuralGuardOnEditedPlan(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	for name, it := range map[string]Item{
		"location root":  {Path: f.userPath("Caches"), Kind: KindCache, Reason: ReasonBundleID},
		"library":        {Path: f.lib, Kind: KindSupport, Reason: ReasonBundleID},
		"home":           {Path: f.home, Kind: KindOther, Reason: ReasonBundleID},
		"nested":         {Path: filepath.Join(a.cache, "com.foo.app"), Kind: KindCache, Reason: ReasonBundleID},
		"wrong location": {Path: a.support, Kind: KindGroupContainer, Reason: ReasonEntitlementGroup},
		"unclean":        {Path: f.userPath("Caches") + "/../Caches/com.foo.app", Kind: KindCache, Reason: ReasonBundleID},
	} {
		t.Run(name, func(t *testing.T) {
			p := f.plan(a.bundle)
			it.Confidence, it.Action = ConfidenceHigh, ActionTrash
			p.Items = append(p.Items, it)
			r, err := f.apply(p, ApplyOptions{})
			if !errors.Is(err, ErrPlanRejected) {
				t.Fatalf("err = %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "protected location") && !strings.Contains(msg, "not directly inside") && !strings.Contains(msg, "clean absolute") {
				t.Fatalf("not rejected by the structural guard: %v", err)
			}
			if len(r.Moved) != 0 || !existsPath(a.bundle) || !existsPath(a.cache) {
				t.Fatal("rejected plan had side effects")
			}
		})
	}
}

// Ported from the review probe: an app that declares another vendor's
// launchd label, app group or AssociatedBundleIdentifiers must not get them.
func TestPlanForeignDeclarationsAreShared(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Evil", "com.evil.app",
		`<key>SMPrivilegedExecutables</key><dict><key>com.vendor.keystone.agent</key><string>x</string></dict>`)
	f.signing(app, "", "group.com.vendor.notes")
	group := f.mkdir(f.userPath("Group Containers", "group.com.vendor.notes"))
	agent := f.launchd(f.userPath("LaunchAgents", "com.vendor.keystone.agent.plist"), "com.vendor.keystone.agent", "/opt/vendor/agent", "")
	tool := f.write(filepath.Join(f.sysLib, "PrivilegedHelperTools", "com.vendor.keystone.agent"), "bin")
	assoc := f.launchd(f.userPath("LaunchAgents", "com.vendor.shared.plist"), "com.vendor.shared", "/opt/vendor/shared",
		`<key>AssociatedBundleIdentifiers</key><array><string>com.evil.app</string><string>com.vendor.app</string></array>`)
	assocLabel := f.launchd(f.userPath("LaunchAgents", "com.vendor.other.plist"), "com.vendor.other", "/opt/vendor/other",
		`<key>AssociatedBundleIdentifiers</key><string>com.evil.app</string>`)

	p := f.plan(app)
	for _, path := range []string{group, agent, tool, assoc, assocLabel} {
		it, ok := itemAt(p, path)
		if ok && (!it.Shared || it.Action != ActionReportOnly) {
			t.Errorf("%s = %+v, want shared report_only", path, it)
		}
	}
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, path := range []string{group, agent, tool, assoc, assocLabel} {
		if !existsPath(path) {
			t.Fatalf("%s was moved", path)
		}
	}
	if calls := f.runner.called("launchctl"); len(calls) != 0 || len(r.Bootouts) != 0 {
		t.Fatalf("foreign agent booted out: %v", calls)
	}
}

func TestPlanOwnedDeclarationsStayOwned(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app",
		`<key>SMPrivilegedExecutables</key><dict><key>com.foo.app.helper</key><string>x</string></dict>`)
	f.signing(app, "ABCDE12345", "group.com.foo.app.shared", "ABCDE12345.com.foo")
	agent := f.launchd(f.userPath("LaunchAgents", "com.foo.app.helper.plist"), "COM.Foo.App.Helper", "/opt/foo/helper", "")
	assoc := f.launchd(f.userPath("LaunchAgents", "com.foo.app.sync.plist"), "com.foo.app.sync", "/opt/foo/sync",
		`<key>AssociatedBundleIdentifiers</key><string>com.foo.app</string>`)
	g1 := f.mkdir(f.userPath("Group Containers", "group.com.foo.app.shared"))
	g2 := f.mkdir(f.userPath("Group Containers", "ABCDE12345.com.foo"))
	p := f.plan(app)
	f.mustItem(p, agent, KindLaunchAgent, ReasonHelperOfBundle, ActionTrash)
	f.mustItem(p, assoc, KindLaunchAgent, ReasonBundleID, ActionTrash)
	f.mustItem(p, g1, KindGroupContainer, ReasonEntitlementGroup, ActionTrash)
	f.mustItem(p, g2, KindGroupContainer, ReasonTeamID, ActionTrash)
}

// Team groups are trusted only for a verified, non-ad-hoc team id.
func TestPlanTeamGroupWithoutVerifiedTeamIsShared(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.signing(app, "ABCDE12345", "ABCDE12345.com.foo.app")
	f.runner.on("", "Signature=adhoc\nTeamIdentifier=ABCDE12345\n", "codesign", "-dv", app)
	g := f.mkdir(f.userPath("Group Containers", "ABCDE12345.com.foo.app"))
	p := f.plan(app)
	if p.App.TeamID != "" {
		t.Fatalf("ad-hoc team = %q", p.App.TeamID)
	}
	if it := f.mustItem(p, g, KindGroupContainer, ReasonEntitlementGroup, ActionReportOnly); !it.Shared {
		t.Fatal("team group without a verified team must be shared")
	}
}

func TestPlanGroupListedBySystemAppIsShared(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.signing(app, "", "group.com.foo.app")
	sys := f.app(filepath.Join(f.root, "System", "Applications", "Utilities"), "Notes", "com.example.notes", "")
	f.signing(sys, "", "GROUP.com.foo.app")
	g := f.mkdir(f.userPath("Group Containers", "group.com.foo.app"))
	p := f.plan(app)
	if it := f.mustItem(p, g, KindGroupContainer, ReasonEntitlementGroup, ActionReportOnly); !it.Shared {
		t.Fatal("group listed by a system app must be shared")
	}
}

// Ported from the review probe: an edited plan pointing at a missing
// bundle was trusted as written.
func TestApplyRefusesMissingBundle(t *testing.T) {
	f := newFixture(t)
	foreign := f.mkdir(f.userPath("Caches", "com.apple.Safari"))
	notes := f.mkdir(f.userPath("Group Containers", "group.com.apple.notes"))
	app := f.app(f.apps, "Gone", "com.gone.app", "")
	p := f.plan(app)
	p.App.Path = filepath.Join(f.apps, "Nope.app")
	p.App.BundleID = "com.apple.Safari"
	p.App.AppGroups = []string{"group.com.apple.notes"}
	p.Items, _ = collect(context.Background(), f.opts.withDefaults(), p.App)

	if _, err := f.apply(p, ApplyOptions{}); !errors.Is(err, ErrBundleMissing) {
		t.Fatalf("err = %v, want ErrBundleMissing", err)
	}
	if _, err := f.apply(p, ApplyOptions{Leftovers: true}); !errors.Is(err, ErrPlanRejected) || !strings.Contains(err.Error(), "com.apple") {
		t.Fatalf("leftovers err = %v, want com.apple refusal", err)
	}
	if !existsPath(foreign) || !existsPath(notes) {
		t.Fatal("refused plan moved data")
	}
}

func TestApplyLeftoversRefusesInstalledIDs(t *testing.T) {
	f := newFixture(t)
	f.app(f.apps, "Bar", "com.bar.app", "")
	f.app(filepath.Join(f.root, "System", "Applications"), "Sys", "com.example.sys", "")
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	base := f.plan(app)
	for _, id := range []string{"com.bar.app", "COM.Bar.App", "com.example.sys"} {
		data := f.mkdir(f.userPath("Application Support", id))
		p := base
		p.App.Path = filepath.Join(f.apps, "Gone.app")
		p.App.BundleID = id
		p.Items = []Item{{Path: data, Kind: KindSupport, Reason: ReasonBundleID, Confidence: ConfidenceHigh, Action: ActionTrash}}
		if _, err := f.apply(p, ApplyOptions{Leftovers: true}); !errors.Is(err, ErrPlanRejected) || !strings.Contains(err.Error(), "installed app") {
			t.Errorf("%s: err = %v, want installed-app refusal", id, err)
		}
		if !existsPath(data) {
			t.Fatalf("%s: data moved", id)
		}
	}
	// --leftovers is refused while the bundle is still there.
	if _, err := f.apply(base, ApplyOptions{Leftovers: true}); !errors.Is(err, ErrPlanRejected) {
		t.Fatalf("installed bundle with --leftovers: err = %v", err)
	}
}

func TestApplyLeftoversActsOnlyOnExactPaths(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.helper(app, "Frameworks", "Foo Helper.app", "com.foo.app.helper")
	f.signing(app, "ABCDE12345", "group.com.foo.app")
	support := f.mkdir(f.userPath("Application Support", "com.foo.app"))
	prefs := f.write(f.userPath("Preferences", "com.foo.app.plist"), "x")
	helperCache := f.mkdir(f.userPath("Caches", "com.foo.app.helper"))
	byHost := f.write(f.userPath("Preferences", "ByHost", "com.foo.app.0123456789AB.plist"), "x")
	group := f.mkdir(f.userPath("Group Containers", "group.com.foo.app"))
	agent := f.launchd(f.userPath("LaunchAgents", "com.foo.app.agent.plist"), "com.foo.app.agent", app+"/Contents/MacOS/Foo", "")
	p := f.plan(app)
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	r, err := f.apply(p, ApplyOptions{Leftovers: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	moved := movedPaths(r)
	if len(moved) != 2 || moved[support] == "" || moved[prefs] == "" || !r.Leftovers {
		t.Fatalf("moved = %v", moved)
	}
	for _, keep := range []string{helperCache, byHost, group, agent} {
		if !existsPath(keep) || !strings.Contains(skippedReason(r, keep), "--leftovers") {
			t.Fatalf("%s: exists=%v reason=%q", keep, existsPath(keep), skippedReason(r, keep))
		}
	}
	if len(f.runner.called("launchctl")) != 0 {
		t.Fatal("leftovers booted out a job")
	}
}

// Ported from the review probe: a second copy with the same bundle id.
func TestPlanDuplicateBundleIDIsShared(t *testing.T) {
	f := newFixture(t)
	f.app(f.apps, "Real", "com.foo.app", "")
	support := f.mkdir(f.userPath("Application Support", "com.foo.app"))
	dup := f.app(filepath.Join(f.home, "Downloads"), "Copy", "com.foo.app", "")
	p := f.plan(dup)
	f.mustItem(p, dup, KindBundle, ReasonBundleID, ActionTrash)
	it := f.mustItem(p, support, KindSupport, ReasonBundleID, ActionReportOnly)
	if !it.Shared || !strings.Contains(it.Note, "same bundle id") {
		t.Fatalf("support = %+v", it)
	}
	if _, err := f.apply(p, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if existsPath(dup) || !existsPath(support) {
		t.Fatal("duplicate's bundle should move and the shared data stay")
	}
}

// Ported from the review probe: com.Foo.App vs an installed com.foo.app on
// a case-insensitive volume.
func TestPlanCaseVariantBundleIDIsShared(t *testing.T) {
	f := newFixture(t)
	f.app(f.apps, "Real", "com.foo.app", "")
	cache := f.mkdir(f.userPath("Caches", "com.foo.app"))
	imposter := f.app(f.apps, "Imposter", "com.Foo.App", "")
	p := f.plan(imposter)
	for _, it := range p.Items {
		if it.Kind != KindBundle && it.Action != ActionReportOnly {
			t.Fatalf("%s = %s, want report_only", it.Path, it.Action)
		}
	}
	if _, err := f.apply(p, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !existsPath(cache) {
		t.Fatal("the other app's cache was moved")
	}
}

func TestReceiptFileNeverMoved(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	tool := f.write(filepath.Join(f.home, "bin", "footool"), "bin") // user-owned
	f.runner.on("pkgid: com.foo.pkg\n", "", "pkgutil", "--file-info", app)
	f.runner.on("volume: "+f.root+"/\nlocation: \n", "", "pkgutil", "--pkg-info", "com.foo.pkg")
	f.runner.on("Applications/Foo.app\nhome/bin/footool\n", "", "pkgutil", "--files", "com.foo.pkg")
	f.runner.on("pkgid: com.foo.pkg\n", "", "pkgutil", "--file-info", tool)
	p := f.plan(app)
	it := f.mustItem(p, tool, KindReceiptFile, ReasonReceipt, ActionReportOnly)
	if !strings.Contains(it.Command, "pkgutil --file-info") {
		t.Fatalf("command = %q", it.Command)
	}
	r, err := f.apply(p, ApplyOptions{IncludeLowConfidence: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, moved := movedPaths(r)[tool]; moved || !existsPath(tool) {
		t.Fatal("receipt file was moved")
	}
}

func TestApplyDedupesPkgutilForget(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.write(filepath.Join(f.receipts, "com.foo.pkg.bom"), "bom")
	f.write(filepath.Join(f.receipts, "com.foo.pkg.plist"), "plist")
	f.runner.on("pkgid: com.foo.pkg\n", "", "pkgutil", "--file-info", app)
	f.runner.on("volume: "+f.root+"/\nlocation: \n", "", "pkgutil", "--pkg-info", "com.foo.pkg")
	f.runner.on("Applications/Foo.app\n", "", "pkgutil", "--files", "com.foo.pkg")
	r, err := f.apply(f.plan(app), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	n := 0
	for _, nr := range r.NeedsRoot {
		if strings.Contains(nr.Command, "pkgutil --forget") {
			n++
		}
	}
	if len(r.NeedsRoot) != 2 || n != 1 {
		t.Fatalf("needs_root = %+v", r.NeedsRoot)
	}
}

// runningUntil models an app that exits once SIGTERM is sent; relaunch
// adds a new process after the first launchctl call.
func (f *fixture) runningUntil(exe string, relaunch bool) {
	start := f.now.Add(-time.Hour)
	f.procs = func() []process.Info {
		switch {
		case relaunch && len(f.runner.called("launchctl")) > 0:
			return []process.Info{{PID: 300, Command: "Foo", ExecutablePath: exe, StartTime: f.now}}
		case len(f.runner.called("kill")) > 0:
			return nil
		}
		return []process.Info{{PID: 100, Command: "Foo", ExecutablePath: exe, StartTime: start}}
	}
	f.runner.on("", "", "kill", "-TERM", "100")
}

func callIndex(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// Ported from the review probe: agents were booted out before the quit, so
// a failed quit left them unloaded.
func TestApplyQuitsBeforeBootout(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.runningUntil(a.bundle+"/Contents/MacOS/Foo", false)
	if _, err := f.apply(p, ApplyOptions{Quit: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	k, l := callIndex(f.runner.calls, "kill"), callIndex(f.runner.calls, "launchctl")
	if k < 0 || l < 0 || k > l {
		t.Fatalf("calls = %v, want kill before launchctl", f.runner.calls)
	}
}

func TestApplyQuitTimeoutLeavesAgentsLoaded(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	start := f.now.Add(-time.Hour)
	f.procs = func() []process.Info {
		return []process.Info{{PID: 100, Command: "Foo", ExecutablePath: a.bundle + "/Contents/MacOS/Foo", StartTime: start}}
	}
	f.runner.on("", "", "kill", "-TERM", "100")
	r, err := f.apply(p, ApplyOptions{Quit: true})
	if !errors.Is(err, ErrQuitTimeout) || !strings.Contains(err.Error(), "no launch agent was booted out") {
		t.Fatalf("err = %v", err)
	}
	if len(f.runner.called("launchctl")) != 0 || len(r.Bootouts) != 0 || len(r.Moved) != 0 || !existsPath(a.agent) {
		t.Fatalf("abort changed state: bootouts=%v moved=%v", r.Bootouts, r.Moved)
	}
}

func TestApplyReportsRelaunchAfterBootout(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.runningUntil(a.bundle+"/Contents/MacOS/Foo", true)
	r, err := f.apply(p, ApplyOptions{Quit: true})
	if !errors.Is(err, ErrRelaunched) {
		t.Fatalf("err = %v, want ErrRelaunched", err)
	}
	if len(r.Relaunched) != 1 || r.Relaunched[0].PID != 300 || len(r.Bootouts) != 1 || len(r.Moved) != 0 || !existsPath(a.bundle) {
		t.Fatalf("result = %+v", r)
	}
}

// renameFailFS fails the rename of one path, as App Management or an
// immutable flag would.
type renameFailFS struct {
	ownerFS
	fail string
}

func (f renameFailFS) Rename(oldpath, newpath string) error {
	if oldpath == f.fail {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EPERM}
	}
	return f.ownerFS.Rename(oldpath, newpath)
}

func TestApplyRootOwnedBundleStopsDataMoves(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	base := f.opts.FS.(ownerFS)
	f.opts.FS = ownerFS{rootOwned: append(append([]string{}, base.rootOwned...), a.bundle)}
	p := f.plan(a.bundle)
	f.mustItem(p, a.bundle, KindBundle, ReasonBundleID, ActionNeedsRoot)
	r, err := f.apply(p, ApplyOptions{})
	if !errors.Is(err, ErrBundleNotMovable) {
		t.Fatalf("err = %v, want ErrBundleNotMovable", err)
	}
	if len(r.Moved) != 0 || !existsPath(a.support) || len(f.runner.called("launchctl")) != 0 {
		t.Fatalf("data moved though the bundle stays: %+v", r.Moved)
	}
	r, err = f.apply(p, ApplyOptions{DataOnly: true})
	if err != nil {
		t.Fatalf("Apply --data-only: %v", err)
	}
	if _, ok := movedPaths(r)[a.support]; !ok || !existsPath(a.bundle) {
		t.Fatalf("--data-only: moved=%v", r.Moved)
	}
}

func TestApplyBundleMoveFailureStopsDataMoves(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	f.opts.FS = renameFailFS{ownerFS: f.opts.FS.(ownerFS), fail: a.bundle}
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.HasPrefix(skippedReason(r, a.bundle), "move failed") || len(r.Moved) != 0 {
		t.Fatalf("moved = %v skipped = %v", r.Moved, r.Skipped)
	}
	if !strings.Contains(skippedReason(r, a.support), "--data-only") || !existsPath(a.support) {
		t.Fatalf("support reason = %q", skippedReason(r, a.support))
	}
	r, err = f.apply(p, ApplyOptions{DataOnly: true})
	if err != nil {
		t.Fatalf("Apply --data-only: %v", err)
	}
	if _, ok := movedPaths(r)[a.support]; !ok {
		t.Fatalf("--data-only did not move data: %v", r.Skipped)
	}
}

// raceFS lets another writer create the first free Trash folder candidate
// just before Apply's Mkdir of it.
type raceFS struct {
	ownerFS
	raced *bool
}

func (f raceFS) Mkdir(path string, perm fs.FileMode) error {
	if !*f.raced && !existsPath(path) {
		*f.raced = true
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(path, "Foo.app"), []byte("racer"), 0o600); err != nil {
			return err
		}
	}
	return f.ownerFS.Mkdir(path, perm)
}

func TestApplyTrashFolderNeverReused(t *testing.T) {
	f := newFixture(t)
	a := f.fooApp()
	p := f.plan(a.bundle)
	name := "com.foo.app uninstalled 2026-11-20 120000"
	pre := f.write(filepath.Join(f.trash, name, "com.foo.app"), "pre-existing")
	raced := false
	f.opts.FS = raceFS{ownerFS: f.opts.FS.(ownerFS), raced: &raced}
	r, err := f.apply(p, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !raced {
		t.Fatal("Mkdir not used for the Trash folder")
	}
	if r.TrashDir != filepath.Join(f.trash, name+" 3") {
		t.Fatalf("trash dir = %q", r.TrashDir)
	}
	if got, _ := os.ReadFile(pre); string(got) != "pre-existing" {
		t.Fatal("pre-existing Trash entry overwritten")
	}
	if got, _ := os.ReadFile(filepath.Join(f.trash, name+" 2", "Foo.app")); string(got) != "racer" {
		t.Fatal("racing Trash entry overwritten")
	}
	for _, m := range r.Moved {
		if filepath.Dir(m.To) != r.TrashDir {
			t.Fatalf("%s moved to %s", m.Path, m.To)
		}
	}
}

func TestPlanSymlinkedBundleIsExplained(t *testing.T) {
	f := newFixture(t)
	sys := f.app(filepath.Join(f.root, "System", "Applications"), "Browser", "com.example.browser", "")
	link := filepath.Join(f.apps, "Browser.app")
	symlink(t, sys, link)
	_, err := BuildPlan(context.Background(), f.opts, link)
	if !errors.Is(err, ErrSystemApp) || !strings.Contains(err.Error(), "symbolic link to "+sys) {
		t.Fatalf("err = %v", err)
	}
	real := f.app(filepath.Join(f.root, "elsewhere"), "Tool", "com.example.tool", "")
	link2 := filepath.Join(f.apps, "Tool.app")
	symlink(t, real, link2)
	if _, err := BuildPlan(context.Background(), f.opts, link2); err == nil || !strings.Contains(err.Error(), "symbolic link to "+real) {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanGenericNamesAreNotMatched(t *testing.T) {
	f := newFixture(t)
	for _, n := range []string{"Updater", "Google", "Helper"} {
		app := f.app(f.apps, n, "com.vendor."+strings.ToLower(n), "")
		dir := f.mkdir(f.userPath("Application Support", n))
		f.mustNotItem(f.plan(app), dir)
	}
}
