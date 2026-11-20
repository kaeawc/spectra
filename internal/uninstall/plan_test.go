package uninstall

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
)

func TestPlanMatchesBundleIDAndDeclaredHelpers(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.helper(app, "Frameworks", "Foo Helper.app", "com.foo.app.helper")
	f.helper(app, "Frameworks", "Squirrel.app", "com.github.Squirrel")
	support := f.mkdir(f.userPath("Application Support", "com.foo.app"))
	helperCache := f.mkdir(f.userPath("Caches", "com.foo.app.helper"))
	prefs := f.write(f.userPath("Preferences", "com.foo.app.plist"), "x")
	byHost := f.write(f.userPath("Preferences", "ByHost", "com.foo.app.0123456789AB-CDEF.plist"), "x")
	container := f.mkdir(f.userPath("Containers", "com.foo.app"))
	saved := f.mkdir(f.userPath("Saved Application State", "com.foo.app.savedState"))
	cookies := f.write(f.userPath("HTTPStorages", "com.foo.app.binarycookies"), "x")
	sysSupport := f.mkdir(filepath.Join(f.sysLib, "Application Support", "com.foo.app"))
	// Lookalikes that must not match.
	unrelated := []string{
		f.mkdir(f.userPath("Caches", "com.github.Squirrel")),
		f.mkdir(f.userPath("Caches", "com.foo.application")),
		f.write(f.userPath("Preferences", "com.foo.application.plist"), "x"),
		f.write(f.userPath("Preferences", "ByHost", "com.foo.app.helper2.0123456789AB.plist"), "x"),
		f.mkdir(f.userPath("Application Support", "com.foo")),
	}
	f.procs = func() []process.Info {
		return []process.Info{
			{PID: 42, Command: "Foo", ExecutablePath: app + "/Contents/MacOS/Foo", StartTime: f.now},
			{PID: 43, Command: "other", ExecutablePath: "/usr/bin/other"},
		}
	}

	p := f.plan(app)
	if p.App.BundleID != "com.foo.app" || strings.Join(p.App.HelperIDs, ",") != "com.foo.app.helper" {
		t.Fatalf("identity = %+v", p.App)
	}
	f.mustItem(p, app, KindBundle, ReasonBundleID, ActionTrash)
	f.mustItem(p, support, KindSupport, ReasonBundleID, ActionTrash)
	f.mustItem(p, helperCache, KindCache, ReasonHelperOfBundle, ActionTrash)
	f.mustItem(p, prefs, KindPreferences, ReasonBundleID, ActionTrash)
	f.mustItem(p, byHost, KindPreferences, ReasonBundleID, ActionTrash)
	f.mustItem(p, container, KindContainer, ReasonBundleID, ActionTrash)
	f.mustItem(p, saved, KindSavedState, ReasonBundleID, ActionTrash)
	f.mustItem(p, cookies, KindHTTPStorage, ReasonBundleID, ActionTrash)
	root := f.mustItem(p, sysSupport, KindSupport, ReasonBundleID, ActionNeedsRoot)
	if root.OwnerUID != 0 || root.RemovableByUser || !strings.HasPrefix(root.Command, "sudo mv -n -- '") {
		t.Fatalf("root item = %+v", root)
	}
	for _, u := range unrelated {
		f.mustNotItem(p, u)
	}
	if len(p.Processes) != 1 || p.Processes[0].PID != 42 {
		t.Fatalf("processes = %+v", p.Processes)
	}
}

func TestPlanLaunchdByProgramAndLabel(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app",
		`<key>SMPrivilegedExecutables</key><dict><key>com.foo.app.privhelper</key><string>identifier x</string></dict>`)
	f.helper(app, "Library/LoginItems", "Foo Login.app", "com.foo.app.login")
	agents := f.userPath("LaunchAgents")
	byProgram := f.launchd(filepath.Join(agents, "whatever.plist"), "com.vendor.updater", app+"/Contents/MacOS/Foo", "")
	byLabel := f.launchd(filepath.Join(agents, "com.foo.app.login.plist"), "com.foo.app.login", "/usr/bin/true", "")
	samePrefix := f.launchd(filepath.Join(agents, "com.foo.other.plist"), "com.foo.other", "/opt/foo/other", "")
	otherApp := f.launchd(filepath.Join(agents, "com.bar.plist"), "com.bar", "/Applications/Foo Pro.app/Contents/MacOS/x", "")
	helperTool := f.write(filepath.Join(f.sysLib, "PrivilegedHelperTools", "com.foo.app.privhelper"), "bin")
	daemon := f.launchd(filepath.Join(f.sysLib, "LaunchDaemons", "com.foo.app.privhelper.plist"), "com.foo.app.privhelper", helperTool, "")
	associated := f.launchd(filepath.Join(f.sysLib, "LaunchDaemons", "com.foo.app.daemon.plist"), "com.foo.app.daemon", "/usr/local/bin/food",
		`<key>AssociatedBundleIdentifiers</key><array><string>com.foo.app</string></array>`)

	p := f.plan(app)
	f.mustItem(p, byProgram, KindLaunchAgent, ReasonLaunchdProgram, ActionTrash)
	f.mustItem(p, byLabel, KindLaunchAgent, ReasonHelperOfBundle, ActionTrash)
	f.mustNotItem(p, samePrefix)
	f.mustNotItem(p, otherApp)
	d := f.mustItem(p, daemon, KindLaunchDaemon, ReasonHelperOfBundle, ActionNeedsRoot)
	if !strings.Contains(d.Command, "sudo launchctl bootout system/'com.foo.app.privhelper'") {
		t.Fatalf("daemon command = %q", d.Command)
	}
	f.mustItem(p, helperTool, KindPrivilegedHelper, ReasonHelperOfBundle, ActionNeedsRoot)
	f.mustItem(p, associated, KindLaunchDaemon, ReasonBundleID, ActionNeedsRoot)
	want := "com.foo.app.daemon,com.foo.app.login,com.foo.app.privhelper,com.vendor.updater"
	if got := strings.Join(p.LaunchdLabels, ","); got != want {
		t.Fatalf("labels = %s, want %s", got, want)
	}
}

func TestPlanReceiptsAndTheirFiles(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	tool := f.write(filepath.Join(f.sysLib, "Foo", "tool"), "bin")
	shared := f.write(filepath.Join(f.sysLib, "Shared", "lib.dylib"), "lib")
	bom := f.write(filepath.Join(f.receipts, "com.foo.pkg.bom"), "bom")
	f.write(filepath.Join(f.receipts, "com.foo.pkg.plist"), "plist")
	otherBom := f.write(filepath.Join(f.receipts, "com.other.pkg.bom"), "bom")
	f.runner.on("volume: /\npath: "+app+"\n\npkgid: com.foo.pkg\npkg-version: 1\n", "", "pkgutil", "--file-info", app)
	f.runner.on("package-id: com.foo.pkg\nvolume: "+f.root+"/\nlocation: \n", "", "pkgutil", "--pkg-info", "com.foo.pkg")
	f.runner.on("Applications\nApplications/Foo.app\nApplications/Foo.app/Contents\nLibrary\nLibrary/Foo\nLibrary/Foo/tool\nLibrary/Shared/lib.dylib\n../etc/passwd\n", "",
		"pkgutil", "--files", "com.foo.pkg")
	f.runner.on("pkgid: com.foo.pkg\n", "", "pkgutil", "--file-info", tool)
	f.runner.on("pkgid: com.foo.pkg\npkgid: com.other.pkg\n", "", "pkgutil", "--file-info", shared)

	p := f.plan(app)
	if strings.Join(p.App.Receipts, ",") != "com.foo.pkg" {
		t.Fatalf("receipts = %v", p.App.Receipts)
	}
	r := f.mustItem(p, bom, KindReceipt, ReasonReceipt, ActionNeedsRoot)
	if r.Command != "sudo pkgutil --forget 'com.foo.pkg'" {
		t.Fatalf("receipt command = %q", r.Command)
	}
	if it := f.mustItem(p, tool, KindReceiptFile, ReasonReceipt, ActionReportOnly); !strings.HasPrefix(it.Command, "pkgutil --file-info '") {
		t.Fatalf("receipt file command = %q", it.Command)
	}
	if plistItem, _ := itemAt(p, filepath.Join(f.receipts, "com.foo.pkg.plist")); plistItem.Command != "" {
		t.Fatalf("pkgutil --forget repeated for the .plist: %q", plistItem.Command)
	}
	if it := f.mustItem(p, shared, KindReceiptFile, ReasonReceipt, ActionReportOnly); !it.Shared {
		t.Fatalf("shared receipt file not shared: %+v", it)
	}
	f.mustNotItem(p, filepath.Join(f.sysLib, "Foo"))
	f.mustNotItem(p, f.sysLib)
	f.mustNotItem(p, otherBom)
}

func TestPlanReceiptNotListingBundleIsIgnored(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	f.write(filepath.Join(f.receipts, "com.foo.pkg.bom"), "bom")
	f.runner.on("pkgid: com.foo.pkg\n", "", "pkgutil", "--file-info", app)
	f.runner.on("volume: "+f.root+"/\nlocation: \n", "", "pkgutil", "--pkg-info", "com.foo.pkg")
	f.runner.on("Applications/Other.app\n", "", "pkgutil", "--files", "com.foo.pkg")
	p := f.plan(app)
	f.mustNotItem(p, filepath.Join(f.receipts, "com.foo.pkg.bom"))
}

func TestPlanGroupContainersOwnedVsShared(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	other := f.app(f.apps, "Bar", "com.foo.bar", "")
	f.signing(app, "ABCDE12345", "ABCDE12345.com.foo.app", "ABCDE12345.suite", "group.com.foo.app", "group.com.foo")
	f.signing(other, "ABCDE12345", "ABCDE12345.suite")
	gc := func(n string) string { return f.mkdir(f.userPath("Group Containers", n)) }
	owned := gc("ABCDE12345.com.foo.app")
	suite := gc("ABCDE12345.suite")
	unlisted := gc("ABCDE12345.unlisted")
	entGroup := gc("group.com.foo.app")
	vendorGroup := gc("group.com.foo")
	otherTeam := gc("ZZZZZ99999.com.foo.app")

	p := f.plan(app)
	if p.App.TeamID != "ABCDE12345" {
		t.Fatalf("team = %q", p.App.TeamID)
	}
	f.mustItem(p, owned, KindGroupContainer, ReasonTeamID, ActionTrash)
	f.mustItem(p, entGroup, KindGroupContainer, ReasonEntitlementGroup, ActionTrash)
	if it := f.mustItem(p, vendorGroup, KindGroupContainer, ReasonEntitlementGroup, ActionReportOnly); !it.Shared {
		t.Fatal("a group that is not group.<bundle id>[.*] must be shared")
	}
	if it := f.mustItem(p, suite, KindGroupContainer, ReasonTeamID, ActionReportOnly); !it.Shared {
		t.Fatal("group listed by another app must be shared")
	}
	if it := f.mustItem(p, unlisted, KindGroupContainer, ReasonTeamID, ActionReportOnly); !it.Shared {
		t.Fatal("team group the app does not list must be shared")
	}
	f.mustNotItem(p, otherTeam)
}

func TestPlanNameMatchIsLowConfidence(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	byName := f.mkdir(f.userPath("Application Support", "Foo"))
	similar := f.mkdir(f.userPath("Application Support", "Foo Pro"))
	p := f.plan(app)
	it := f.mustItem(p, byName, KindSupport, ReasonName, ActionReportOnly)
	if it.Confidence != ConfidenceLow {
		t.Fatalf("confidence = %s", it.Confidence)
	}
	f.mustNotItem(p, similar)
}

func TestPlanDoesNotMatchSimilarlyNamedApps(t *testing.T) {
	f := newFixture(t)
	code := f.app(f.apps, "Code", "com.example.code", "")
	vscode := f.app(f.apps, "Visual Studio Code", "com.microsoft.VSCode", "")
	// VS Code's CFBundleName is "Code", which claims the bare name.
	f.write(filepath.Join(vscode, "Contents", "Info.plist"), plistXML(map[string]string{
		"CFBundleIdentifier": "com.microsoft.VSCode", "CFBundleName": "Code",
	}, ""))
	f.app(f.apps, "Xcode", "com.apple.dt.Xcode", "")
	leftovers := []string{
		f.mkdir(f.userPath("Application Support", "Code")),
		f.mkdir(f.userPath("Application Support", "Xcode")),
		f.mkdir(f.userPath("Application Support", "Visual Studio Code")),
		f.mkdir(f.userPath("Caches", "com.microsoft.VSCode")),
		f.mkdir(f.userPath("Caches", "com.apple.dt.Xcode")),
		f.mkdir(f.userPath("Caches", "com.example.codesign")),
	}
	p := f.plan(code)
	for _, l := range leftovers {
		f.mustNotItem(p, l)
	}
	if len(p.Items) != 1 || p.Items[0].Kind != KindBundle {
		t.Fatalf("items = %v", paths(p))
	}
}

func TestPlanRefusesSystemApp(t *testing.T) {
	f := newFixture(t)
	calc := f.app(filepath.Join(f.root, "System", "Applications"), "Calculator", "com.apple.calculator", "")
	_, err := BuildPlan(context.Background(), f.opts, calc)
	if !errors.Is(err, ErrSystemApp) {
		t.Fatalf("err = %v, want ErrSystemApp", err)
	}
}

func TestPlanResolvesBundleID(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	if p := f.plan("com.foo.app"); p.App.Path != app {
		t.Fatalf("path = %s", p.App.Path)
	}
	if _, err := BuildPlan(context.Background(), f.opts, "com.missing.app"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanUnsupportedOffDarwin(t *testing.T) {
	f := newFixture(t)
	f.opts.OS = hostos.Linux
	if _, err := BuildPlan(context.Background(), f.opts, "com.foo.app"); !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Apply(context.Background(), f.opts, Plan{}, ApplyOptions{}); !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("apply err = %v", err)
	}
}

func TestPlanSymlinkItemIsTheLink(t *testing.T) {
	f := newFixture(t)
	app := f.app(f.apps, "Foo", "com.foo.app", "")
	target := f.write(filepath.Join(f.root, "elsewhere", "big"), strings.Repeat("x", 1<<16))
	link := f.userPath("Caches", "com.foo.app")
	f.mkdir(filepath.Dir(link))
	symlink(t, filepath.Dir(target), link)
	p := f.plan(app)
	it := f.mustItem(p, link, KindCache, ReasonBundleID, ActionTrash)
	if !it.Symlink || it.SizeBytes >= 1<<16 {
		t.Fatalf("symlink item = %+v", it)
	}
}
