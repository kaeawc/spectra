package startup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestParsePrintDisabled(t *testing.T) {
	// The macOS 26 `launchctl print-disabled gui/501` format, plus the
	// true/false form older releases print.
	out := `
	disabled services = {
		"com.example.helper" => enabled
		"com.apple.example.agent" => disabled
		"com.example.old-off" => true
		"com.example.old-on" => false
		"com.example.weird" => maybe
		not a line
	}
	login item associations = {
	}
`
	got := parsePrintDisabled([]byte(out))
	want := map[string]bool{
		"com.example.helper":      false,
		"com.apple.example.agent": true,
		"com.example.old-off":     true,
		"com.example.old-on":      false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseDomainServices(t *testing.T) {
	out := `gui/501 = {
	type = login
	environment = {
		SSH_AUTH_SOCK => /var/run/x
	}

	services = {
		    1160      - 	application.com.example.notes.1000000001.1000000002
		       0      0 	com.example.chat.ShipIt
		       0     78 	com.example.failed
	}

	disabled services = {
		"com.apple.example.daemon" => disabled
	}
}`
	got := parseDomainServices([]byte(out))
	want := map[string]serviceState{
		"application.com.example.notes.1000000001.1000000002": {PID: 1160},
		"com.example.chat.ShipIt":                             {PID: 0, LastExit: "0"},
		"com.example.failed":                                  {PID: 0, LastExit: "78"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// readBTMFixture returns a synthetic `sfltool dumpbtm` capture: made-up apps
// in the real output's format, one per record shape the parser handles.
func readBTMFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "dumpbtm.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseDumpBTMFixture(t *testing.T) {
	items := parseDumpBTM([]byte(readBTMFixture(t)))
	ids := map[string]btmItem{}
	for _, it := range items {
		if it.Type == "developer" {
			t.Fatalf("developer item kept: %+v", it)
		}
		ids[it.Identifier] = it
	}
	vpnd := ids["16.com.example.vpnd"]
	if vpnd.UID != -2 || vpnd.Type != "legacy daemon" || vpnd.TeamID != "BCDEF23456" ||
		vpnd.ExecutablePath != "/Library/PrivilegedHelperTools/com.example.vpnd" ||
		vpnd.plistPath() != "/Library/LaunchDaemons/com.example.vpnd.plist" ||
		!reflect.DeepEqual(vpnd.AssocBundleIDs, []string{"com.example.vpn"}) ||
		!reflect.DeepEqual(vpnd.Disposition, []string{"enabled", "allowed", "notified"}) {
		t.Fatalf("vpnd = %+v", vpnd)
	}
	if vpnd.label() != "com.example.vpnd" || vpnd.scope() != ScopeSystem {
		t.Fatalf("label/scope = %q/%q", vpnd.label(), vpnd.scope())
	}
	helper := ids["8.com.example.chat.helper"]
	if helper.UID != 501 || helper.Type != "agent" || helper.URL != "Contents/Library/LaunchAgents/com.example.chat.helper.plist" {
		t.Fatalf("chat helper = %+v", helper)
	}
	byID := map[string]btmItem{}
	for _, it := range items {
		byID[it.Identifier] = it
	}
	helper.resolve(byID)
	if helper.ExecutablePath != "/Applications/Example Chat Classic.app/Contents/Resources/ExampleChatHelper" {
		t.Fatalf("resolved exe = %q", helper.ExecutablePath)
	}
	if helper.plistPath() != "/Applications/Example Chat Classic.app/Contents/Library/LaunchAgents/com.example.chat.helper.plist" {
		t.Fatalf("resolved plist = %q", helper.plistPath())
	}
	if ids["2.com.example.notes"].Name != "Example Notes" || ids["2.com.example.notes"].startsAtLogin() {
		t.Fatalf("disabled parent app should not start at login: %+v", ids["2.com.example.notes"])
	}
	if !ids["2.com.example.clipboard"].startsAtLogin() {
		t.Fatal("enabled app (open at login) should start at login")
	}
	for _, id := range []string{"2048.com.example.studio.QuickLook", "4096.com.example.studio.Spotlight", "8192.com.example.studio.DockTile"} {
		if it, ok := ids[id]; !ok || it.startsAtLogin() {
			t.Fatalf("plug-in %s should parse and not start at login: %+v", id, it)
		}
	}
	if ids["Unknown Developer"].Identifier != "" {
		t.Fatal("developer group kept")
	}
	if ids["2.com.example.meet"].DeveloperName != "" {
		t.Fatalf("(null) should parse as empty: %q", ids["2.com.example.meet"].DeveloperName)
	}
	if other := ids["8.com.example.otheruser"]; other.UID != 502 {
		t.Fatalf("other user's agent = %+v", other)
	}
}

func TestParseDumpBTMIgnoresGarbage(t *testing.T) {
	if got := parseDumpBTM([]byte("sfltool: something\n#1:\nno colon here\n")); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := parseDumpBTM(nil); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestListPlistsTriggersAndState(t *testing.T) {
	tr := newTree(t)
	writeJob(t, tr.userAgents, "com.example.args", `<key>ProgramArguments</key><array><string>/opt/bin/tool</string><string>--serve</string></array><key>RunAtLoad</key><true/>`)
	writeJob(t, tr.userAgents, "com.example.keep", `<key>Program</key><string>/opt/bin/keep</string><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>RunAtLoad</key><true/>`)
	writeJob(t, tr.userAgents, "com.example.interval", `<key>Program</key><string>/opt/bin/i</string><key>StartInterval</key><integer>3600</integer>`)
	writeJob(t, tr.userAgents, "com.example.cal", `<key>Program</key><string>/opt/bin/c</string><key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer></dict>`)
	writeJob(t, tr.userAgents, "com.example.demand", `<key>Program</key><string>/opt/bin/d</string><key>Disabled</key><true/>`)
	writeJob(t, tr.libDaemons, "com.example.daemon", `<key>Program</key><string>/opt/bin/daemon</string><key>RunAtLoad</key><true/>`)
	writeFile(t, filepath.Join(tr.userAgents, "broken.plist"), "<plist><dict><key>Label")
	writeFile(t, filepath.Join(tr.userAgents, "nolabel.plist"), `<?xml version="1.0"?><plist version="1.0"><dict/></plist>`)
	writeFile(t, filepath.Join(tr.userAgents, "README.txt"), "ignored")

	f := newFakeLaunchd()
	gui := "gui/501"
	f.disabled[gui]["com.example.interval"] = true
	f.disabled[gui]["com.example.demand"] = false // override beats the plist key
	f.loaded[gui]["com.example.args"] = serviceState{PID: 77}
	f.loaded[gui]["com.example.keep"] = serviceState{LastExit: "1"}
	f.loaded["system"]["com.example.daemon"] = serviceState{PID: 9}

	rep, err := List(context.Background(), tr.opts(f))
	if err != nil {
		t.Fatal(err)
	}
	got := byLabel(rep.Items)
	if len(rep.Items) != 6 {
		t.Fatalf("items = %v", labels(rep.Items))
	}
	args := got["com.example.args"]
	if args.Program != "/opt/bin/tool" || !reflect.DeepEqual(args.ProgramArguments, []string{"/opt/bin/tool", "--serve"}) ||
		args.Trigger != TriggerLogin || !args.Running || args.PID != 77 || args.Scope != ScopeUser ||
		args.Domain != gui || args.PlistOwner != "tester" || args.Kind != KindLaunchAgent {
		t.Fatalf("args = %+v", args)
	}
	if k := got["com.example.keep"]; k.Trigger != TriggerKeepAlive || !reflect.DeepEqual(k.Triggers, []string{TriggerKeepAlive, TriggerLogin}) || !k.Loaded || k.Running || k.LastExit != "1" {
		t.Fatalf("keep = %+v", k)
	}
	if i := got["com.example.interval"]; i.Trigger != TriggerInterval || !i.Disabled || i.DisabledBy != "launchctl override" || i.Loaded {
		t.Fatalf("interval = %+v", i)
	}
	if c := got["com.example.cal"]; c.Trigger != TriggerCalendar {
		t.Fatalf("cal = %+v", c)
	}
	if d := got["com.example.demand"]; d.Trigger != TriggerOnDemand || d.Disabled {
		t.Fatalf("demand = %+v", d)
	}
	if d := got["com.example.daemon"]; d.Trigger != TriggerBoot || d.Scope != ScopeSystem || d.Domain != "system" || d.PID != 9 || d.Kind != KindLaunchDaemon {
		t.Fatalf("daemon = %+v", d)
	}
	if len(rep.Errors) != 2 {
		t.Fatalf("errors = %+v", rep.Errors)
	}
	for _, e := range rep.Errors {
		if !strings.HasSuffix(e.Path, "broken.plist") && !strings.HasSuffix(e.Path, "nolabel.plist") {
			t.Fatalf("unexpected error %+v", e)
		}
	}
	btm := sourceStatus(rep, SourceBTM)
	if btm.Status != StatusNeedsRoot {
		t.Fatalf("btm source = %+v", btm)
	}
	for _, c := range f.Calls() {
		if c.Name == sfltoolPath {
			t.Fatal("sfltool must not run without root: it prompts for authorization")
		}
	}

	userOnly := tr.opts(f)
	userOnly.Scope = "user"
	rep, err = List(context.Background(), userOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Scope != ScopeUser {
			t.Fatalf("--scope user returned %+v", it)
		}
	}
}

func sourceStatus(rep Report, name string) SourceStatus {
	for _, s := range rep.Sources {
		if s.Name == name {
			return s
		}
	}
	return SourceStatus{}
}

func TestListSkipsAppleDirsUnlessIncluded(t *testing.T) {
	tr := newTree(t)
	sys := filepath.Join(tr.root, "System", "Library", "LaunchAgents")
	writeJob(t, sys, "com.apple.thing", `<key>RunAtLoad</key><true/>`)
	opts := tr.opts(newFakeLaunchd())
	opts.PlistDirs = append(opts.PlistDirs, PlistDir{Path: sys, Scope: ScopeSystem, Kind: KindLaunchAgent, Apple: true})
	rep, err := List(context.Background(), opts)
	if err != nil || len(rep.Items) != 0 {
		t.Fatalf("default list = %v, %v", labels(rep.Items), err)
	}
	opts.IncludeApple = true
	rep, err = List(context.Background(), opts)
	if err != nil || len(rep.Items) != 1 || !rep.Items[0].Apple {
		t.Fatalf("--include-apple list = %+v, %v", rep.Items, err)
	}
}

func TestListShowsDuplicateLabelsAsSeparateRows(t *testing.T) {
	tr := newTree(t)
	user := writeJob(t, tr.userAgents, "com.example.dup", `<key>Program</key><string>/opt/mine</string>`)
	global := writeJob(t, tr.libAgents, "com.example.dup", `<key>Program</key><string>/opt/global</string>`)
	rep, err := List(context.Background(), tr.opts(newFakeLaunchd()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 2 {
		t.Fatalf("items = %+v", rep.Items)
	}
	for _, it := range rep.Items {
		other := global
		if it.PlistPath == global {
			other = user
		}
		if it.Domain != "gui/501" || !reflect.DeepEqual(it.DuplicateOf, []string{other}) {
			t.Fatalf("row %s: domain %s duplicate_of %v, want [%s]", it.PlistPath, it.Domain, it.DuplicateOf, other)
		}
	}
}

func TestListMergesBTMWithoutDuplicates(t *testing.T) {
	tr := newTree(t)
	// Same labels as BTM items in the fixture, at different paths, so the
	// label match (not the path match) must merge them.
	writeJob(t, tr.userAgents, "com.example.toolbox", `<key>Program</key><string>/Applications/Example Toolbox.app/Contents/MacOS/example-toolbox</string><key>RunAtLoad</key><true/>`)
	writeJob(t, tr.libAgents, "com.example.meet.updater", `<key>Program</key><string>/opt/meet</string><key>StartInterval</key><integer>60</integer>`)
	f := newFakeLaunchd()
	f.btm = readBTMFixture(t)
	f.loaded["gui/501"]["com.example.notes.launcher"] = serviceState{PID: 31}
	f.disabled["gui/501"]["com.example.notes.launcher"] = true // BTM disposition wins for BTM-only items
	opts := tr.opts(f)
	opts.EUID = func() int { return 0 }
	opts.Getenv = func(k string) string {
		if k == "SUDO_UID" {
			return "501"
		}
		return ""
	}
	rep, err := List(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.UID != 501 {
		t.Fatalf("uid = %d, want SUDO_UID 501", rep.UID)
	}
	seen := map[string]int{}
	for _, it := range rep.Items {
		seen[it.Label]++
	}
	for l, n := range seen {
		if n > 1 {
			t.Fatalf("label %s appears %d times", l, n)
		}
	}
	want := []string{
		"ABCDE12345.com.example.notes.browser-helper", "com.example.autoupdate", "com.example.chat.helper",
		"com.example.clipboard", "com.example.meet.daemon", "com.example.meet.updater",
		"com.example.meet.updater.login.check", "com.example.notes.launcher", "com.example.toolbox", "com.example.vpnd",
	}
	if got := labels(rep.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels =\n%v\nwant\n%v", got, want)
	}
	got := byLabel(rep.Items)
	tb := got["com.example.toolbox"]
	if tb.Kind != KindLaunchAgent || tb.BTM == nil || !reflect.DeepEqual(tb.Sources[:2], []string{SourcePlist, SourceBTM}) ||
		tb.Name != "Example Toolbox" || tb.Owner == nil || tb.Owner.TeamID == "" {
		t.Fatalf("toolbox = %+v owner %+v", tb, tb.Owner)
	}
	launcher := got["com.example.notes.launcher"]
	if launcher.Kind != KindLoginItem || launcher.PlistPath != "" || launcher.Disabled || !launcher.Running || launcher.PID != 31 ||
		launcher.Owner == nil || launcher.Owner.App != "Example Notes" || launcher.Owner.TeamID != "ABCDE12345" || launcher.Owner.Via != "btm" {
		t.Fatalf("launcher = %+v owner %+v", launcher, launcher.Owner)
	}
	if d := got["com.example.vpnd"]; d.Scope != ScopeSystem || d.Domain != "system" || d.Trigger != TriggerBoot ||
		d.Owner == nil || d.Owner.TeamID != "BCDEF23456" || d.Owner.Developer != "Example Network Ltd" {
		t.Fatalf("vpnd = %+v owner %+v", d, d.Owner)
	}
	if d := got["com.example.meet.daemon"]; d.Owner == nil || d.Owner.App != "Example Meet" || d.Owner.Developer != "Example Meet Inc." {
		t.Fatalf("meet daemon owner = %+v", d.Owner)
	}
	if a := got["com.example.clipboard"]; a.Program != "/Applications/Example Clipboard 2.app" || a.Owner == nil || a.Owner.BundlePath != "/Applications/Example Clipboard 2.app" {
		t.Fatalf("clipboard = %+v", a)
	}
	if s := sourceStatus(rep, SourceBTM); s.Status != StatusOK {
		t.Fatalf("btm source = %+v", s)
	}
}

func TestListBTMSkipsOtherUsers(t *testing.T) {
	tr := newTree(t)
	f := newFakeLaunchd()
	f.btm = ` Records for UID 502 : X
 #1:
                 Name: theirs
                 Type: legacy agent (0x10008)
          Disposition: [enabled, allowed, notified] (0xb)
           Identifier: 8.com.example.theirs
 Records for UID 501 : Y
 #1:
                 Name: mine
                 Type: legacy agent (0x10008)
          Disposition: [disabled, allowed, notified] (0xa)
           Identifier: 8.com.example.mine
`
	opts := tr.opts(f)
	opts.EUID = func() int { return 0 }
	opts.Getenv = func(string) string { return "501" }
	rep, err := List(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 1 || rep.Items[0].Label != "com.example.mine" || !rep.Items[0].Disabled || rep.Items[0].DisabledBy != "btm disposition" {
		t.Fatalf("items = %+v", rep.Items)
	}
}

func TestOwnerAttribution(t *testing.T) {
	tr := newTree(t)
	app := writeApp(t, tr.apps, "Widget", "com.example.widget")
	writeApp(t, tr.apps, "WidgetPro", "com.example.widget.pro")
	nested := filepath.Join(tr.apps, "Vendor")
	writeApp(t, nested, "Other", "com.other.app")
	writeJob(t, tr.userAgents, "com.example.inbundle", `<key>Program</key><string>`+app+`/Contents/MacOS/Widget</string>`)
	writeJob(t, tr.userAgents, "com.example.widget.pro.helper", `<key>Program</key><string>/opt/helper</string>`)
	writeJob(t, tr.userAgents, "com.example.widget", `<key>Program</key><string>/opt/w</string>`)
	writeJob(t, tr.userAgents, "net.assoc.job", `<key>Program</key><string>/opt/a</string><key>AssociatedBundleIdentifiers</key><array><string>com.other.app</string></array>`)
	writeJob(t, tr.userAgents, "org.nobody.job", `<key>Program</key><string>/opt/n</string>`)

	f := newFakeLaunchd()
	rep, err := List(context.Background(), tr.opts(f))
	if err != nil {
		t.Fatal(err)
	}
	got := byLabel(rep.Items)
	check := func(label, app, via string) {
		t.Helper()
		o := got[label].Owner
		if o == nil || o.App != app || o.Via != via || o.TeamID != "TEAM123456" {
			t.Fatalf("%s owner = %+v, want %s via %s", label, o, app, via)
		}
	}
	check("com.example.inbundle", "Widget", "program_in_bundle")
	check("com.example.widget.pro.helper", "WidgetPro", "bundle_id_prefix")
	check("com.example.widget", "Widget", "bundle_id_prefix")
	check("net.assoc.job", "Other", "associated_bundle_identifier")
	if o := got["org.nobody.job"].Owner; o != nil {
		t.Fatalf("unowned job got owner %+v", o)
	}
}

func TestListRunsNoMutatingCommand(t *testing.T) {
	tr := newTree(t)
	writeApp(t, tr.apps, "Widget", "com.example.widget")
	writeJob(t, tr.userAgents, "com.example.widget.agent", `<key>Program</key><string>/opt/w</string><key>RunAtLoad</key><true/>`)
	writeJob(t, tr.libDaemons, "com.example.d", `<key>Program</key><string>/opt/d</string>`)
	f := newFakeLaunchd()
	f.btm = readBTMFixture(t)
	for _, euid := range []int{testUID, 0} {
		opts := tr.opts(f)
		opts.EUID = func() int { return euid }
		opts.IncludeApple = true
		if _, err := List(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}
	if m := mutatingCalls(f.Calls()); len(m) != 0 {
		t.Fatalf("list issued mutating commands: %v", m)
	}
}

func TestListUnsupportedOffDarwin(t *testing.T) {
	f := newFakeLaunchd()
	_, err := List(context.Background(), Options{OS: hostos.Linux, Runner: f})
	if !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	_, err = Disable(context.Background(), Options{OS: hostos.Linux, Runner: f}, "x", "")
	if !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("disable err = %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("calls off darwin: %v", f.Calls())
	}
}

func TestReportJSONShape(t *testing.T) {
	tr := newTree(t)
	writeJob(t, tr.userAgents, "com.example.j", `<key>Program</key><string>/opt/j</string><key>RunAtLoad</key><true/>`)
	rep, err := List(context.Background(), tr.opts(newFakeLaunchd()))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"uid", "items", "sources", "collected_at"} {
		if _, ok := v[k]; !ok {
			t.Fatalf("report JSON missing %q: %s", k, data)
		}
	}
	item := v["items"].([]any)[0].(map[string]any)
	for _, k := range []string{"label", "kind", "scope", "domain", "plist_path", "program", "trigger", "triggers", "disabled", "loaded", "running", "sources"} {
		if _, ok := item[k]; !ok {
			t.Fatalf("item JSON missing %q: %v", k, item)
		}
	}
	if _, ok := item["assoc"]; ok {
		t.Fatal("unexported assoc leaked into JSON")
	}
}
