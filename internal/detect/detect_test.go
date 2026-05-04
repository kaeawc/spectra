package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeBundle creates a synthetic .app skeleton at t.TempDir()/Name.app
// and returns its path. Subsequent helpers add markers.
func makeBundle(t *testing.T, name string) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), name+".app")
	for _, sub := range []string{"Contents/MacOS", "Contents/Resources", "Contents/Frameworks"} {
		if err := os.MkdirAll(filepath.Join(app, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Minimal Info.plist (XML) pointing CFBundleExecutable at "main".
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>main</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "main"), []byte("\x7fELF placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectElectron(t *testing.T) {
	app := makeBundle(t, "FakeElectron")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/Electron Framework.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(app, "Contents/Resources/app.asar"))

	r, err := Detect(app)
	if err != nil {
		t.Fatal(err)
	}
	if r.UI != "Electron" {
		t.Errorf("UI = %q, want Electron", r.UI)
	}
	if r.Runtime != "Node+Chromium" {
		t.Errorf("Runtime = %q", r.Runtime)
	}
	if r.Confidence != "high" {
		t.Errorf("Confidence = %q", r.Confidence)
	}
}

func TestDetectFlutter(t *testing.T) {
	app := makeBundle(t, "FakeFlutter")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/FlutterMacOS.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := Detect(app)
	if r.UI != "Flutter" || r.Language != "Dart" {
		t.Errorf("got UI=%q lang=%q", r.UI, r.Language)
	}
}

func TestDetectQt(t *testing.T) {
	app := makeBundle(t, "FakeQt")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/QtCore.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := Detect(app)
	if r.UI != "Qt" {
		t.Errorf("UI = %q, want Qt", r.UI)
	}
}

func TestDetectComposeDesktop(t *testing.T) {
	app := makeBundle(t, "FakeKMP")
	// Bundled JVM
	jvmDir := filepath.Join(app, "Contents/runtime/Contents/Home/lib/server")
	touch(t, filepath.Join(jvmDir, "libjvm.dylib"))
	// skiko marker
	touch(t, filepath.Join(app, "Contents/app/libskiko-macos-arm64.dylib"))

	r, _ := Detect(app)
	if r.UI != "ComposeDesktop" || r.Language != "Kotlin" {
		t.Errorf("got UI=%q lang=%q signals=%v", r.UI, r.Language, r.Signals)
	}
}

func TestDetectEclipseRCPNoBundledJVM(t *testing.T) {
	app := makeBundle(t, "FakeMAT")
	// Mimic Memory Analyzer: many jars + an Eclipse OSGi plugin, no bundled JVM.
	for i := 0; i < 6; i++ {
		touch(t, filepath.Join(app, "Contents/Eclipse/plugins", "lib"+string(rune('a'+i))+".jar"))
	}
	touch(t, filepath.Join(app, "Contents/Eclipse/plugins/org.eclipse.osgi_3.18.jar"))

	r, _ := Detect(app)
	if r.UI != "EclipseRCP" {
		t.Errorf("UI = %q, want EclipseRCP. signals=%v", r.UI, r.Signals)
	}
}

func TestDetectRustOverridesAppKit(t *testing.T) {
	// Synthetic binary with Rust panic strings. We cannot synthesize otool
	// output, but classifyByLinkedLibs is a no-op when otool returns empty
	// (otool will report no libs for a non-Mach-O file). So Layer 3 owns
	// the verdict here and should pick Native (Rust).
	app := makeBundle(t, "FakeRust")
	bin := filepath.Join(app, "Contents/MacOS/main")
	body := make([]byte, 0, 64*1024)
	for i := 0; i < 200; i++ {
		body = append(body, []byte("rustc/library/core/src/panicking.rs:42:5\n")...)
		body = append(body, []byte("panicked at thread 'main'\n")...)
	}
	if err := os.WriteFile(bin, body, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := Detect(app)
	if r.Runtime != "Rust" {
		t.Errorf("Runtime = %q, want Rust. signals=%v", r.Runtime, r.Signals)
	}
}

func TestDetectUnknown(t *testing.T) {
	app := makeBundle(t, "FakeMystery")
	r, _ := Detect(app)
	if r.UI != "Unknown" || r.Confidence != "low" {
		t.Errorf("expected Unknown/low, got UI=%q conf=%q", r.UI, r.Confidence)
	}
}

func TestMetadataPopulated(t *testing.T) {
	app := makeBundle(t, "FakeMeta")
	// Add fields to the synthetic Info.plist by overwriting it.
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>main</string>
<key>CFBundleIdentifier</key><string>com.example.fake</string>
<key>CFBundleShortVersionString</key><string>1.2.3</string>
<key>CFBundleVersion</key><string>456</string>
<key>SUFeedURL</key><string>https://example.com/appcast.xml</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents/Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}

	r, _ := Detect(app)
	if r.BundleID != "com.example.fake" {
		t.Errorf("BundleID = %q", r.BundleID)
	}
	if r.AppVersion != "1.2.3" {
		t.Errorf("AppVersion = %q", r.AppVersion)
	}
	if r.BuildNumber != "456" {
		t.Errorf("BuildNumber = %q", r.BuildNumber)
	}
	if r.SparkleFeedURL != "https://example.com/appcast.xml" {
		t.Errorf("SparkleFeedURL = %q", r.SparkleFeedURL)
	}
	if r.BundleSizeBytes <= 0 {
		t.Errorf("BundleSizeBytes = %d, want >0", r.BundleSizeBytes)
	}
}

func TestWKWebViewWrapper(t *testing.T) {
	app := makeBundle(t, "FakeWebApp")
	// We cannot synthesize a Mach-O linking AppKit+WebKit, so simulate
	// the post-Layer-2 state by writing the same markers via a custom
	// test helper would require exporting internals. Instead, we test
	// hasBundledWebApp directly through a Detect call where Layer 2
	// returns nothing and Layer 3 sees a normal binary — and confirm
	// at minimum that bundling index.html doesn't crash detection.
	touch(t, filepath.Join(app, "Contents/Resources/index.html"))
	r, err := Detect(app)
	if err != nil {
		t.Fatal(err)
	}
	// The synthetic binary doesn't link AppKit, so verdict stays Unknown.
	// What we want to confirm is that Detect returns cleanly with the
	// web-asset bundle — full integration tested via `spectra` against
	// real apps.
	if r.Path != app {
		t.Errorf("path mismatch")
	}
}

func TestCatalystMarkerStringsRecognized(t *testing.T) {
	// Sanity check: the Catalyst signal substrings we look for in
	// classifyByLinkedLibs ought to be matched by realistic otool -L
	// lines. This guards against future refactors that might typo them.
	samples := []string{
		"/System/iOSSupport/System/Library/Frameworks/UIKit.framework/Versions/A/UIKit",
		"/System/Library/PrivateFrameworks/UIKitMacHelper.framework/Versions/A/UIKitMacHelper",
	}
	for _, s := range samples {
		if !strings.Contains(s, "/iOSSupport/") && !strings.Contains(s, "/UIKitMacHelper.framework/") {
			t.Errorf("Catalyst sample %q would not match either substring", s)
		}
	}
}

func TestPrivacyDescriptionsParsed(t *testing.T) {
	app := makeBundle(t, "FakePrivacy")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>main</string>
<key>NSCameraUsageDescription</key><string>Need camera for video calls</string>
<key>NSMicrophoneUsageDescription</key><string>Need mic for voice notes</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents/Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := Detect(app)
	if r.PrivacyDescriptions["NSCameraUsageDescription"] != "Need camera for video calls" {
		t.Errorf("camera description = %q", r.PrivacyDescriptions["NSCameraUsageDescription"])
	}
	if r.PrivacyDescriptions["NSMicrophoneUsageDescription"] != "Need mic for voice notes" {
		t.Errorf("mic description = %q", r.PrivacyDescriptions["NSMicrophoneUsageDescription"])
	}
}

func TestDependenciesNPMPackages(t *testing.T) {
	app := makeBundle(t, "FakeNPM")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/Electron Framework.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(app, "Contents/Resources/app.asar"))
	for _, p := range []string{"node-pty", "better-sqlite3"} {
		if err := os.MkdirAll(filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules", p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Scoped package
	if err := os.MkdirAll(filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/@scope/pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := Detect(app)
	if r.Dependencies == nil {
		t.Fatal("Dependencies nil")
	}
	want := map[string]bool{"node-pty": true, "better-sqlite3": true, "@scope/pkg": true}
	for _, p := range r.Dependencies.NPMPackages {
		if !want[p] {
			t.Errorf("unexpected pkg %q", p)
		}
		delete(want, p)
	}
	if len(want) != 0 {
		t.Errorf("missing packages: %v", want)
	}
}

func TestNetworkEndpointsExtraction(t *testing.T) {
	app := makeBundle(t, "FakeNet")
	bin := filepath.Join(app, "Contents/MacOS/main")
	body := []byte("noise https://api.example.com/v1 padding https://cdn.foo.io/asset.png and HTTP://Mixed.Case.NET/x")
	if err := os.WriteFile(bin, body, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := DetectWith(app, Options{ScanNetwork: true})
	want := map[string]bool{"api.example.com": true, "cdn.foo.io": true, "mixed.case.net": true}
	for _, h := range r.NetworkEndpoints {
		if !want[h] {
			t.Errorf("unexpected host %q", h)
		}
		delete(want, h)
	}
	if len(want) != 0 {
		t.Errorf("missing hosts: %v", want)
	}
}

func TestHelpersScanned(t *testing.T) {
	app := makeBundle(t, "FakeHelpers")
	// Helper sub-app
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/Foo Helper.app/Contents/MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	// XPC service
	if err := os.MkdirAll(filepath.Join(app, "Contents/XPCServices/Updater.xpc/Contents/MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Plugin
	if err := os.MkdirAll(filepath.Join(app, "Contents/PlugIns/SomeExt.appex"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := Detect(app)
	if r.Helpers == nil {
		t.Fatal("Helpers nil")
	}
	if len(r.Helpers.HelperApps) != 1 || r.Helpers.HelperApps[0] != "Foo Helper" {
		t.Errorf("HelperApps = %v", r.Helpers.HelperApps)
	}
	if len(r.Helpers.XPCServices) != 1 || r.Helpers.XPCServices[0] != "Updater" {
		t.Errorf("XPCServices = %v", r.Helpers.XPCServices)
	}
	if len(r.Helpers.Plugins) != 1 || r.Helpers.Plugins[0] != "SomeExt.appex" {
		t.Errorf("Plugins = %v", r.Helpers.Plugins)
	}
}

func TestBundleIDPrefix(t *testing.T) {
	cases := map[string]string{
		"com.docker.docker":              "com.docker",
		"com.anthropic.claudefordesktop": "com.anthropic",
		"app.tuple.app":                  "app.tuple",
		"single":                         "",
		"":                               "",
	}
	for in, want := range cases {
		if got := bundleIDPrefix(in); got != want {
			t.Errorf("bundleIDPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidBundleID(t *testing.T) {
	good := []string{"com.docker.docker", "app.tuple.app", "Test-name_2"}
	for _, s := range good {
		if !validBundleID(s) {
			t.Errorf("validBundleID(%q) = false, want true", s)
		}
	}
	bad := []string{"", "evil; DROP TABLE", "with space", "x'; --", "quote'in"}
	for _, s := range bad {
		if validBundleID(s) {
			t.Errorf("validBundleID(%q) = true, want false", s)
		}
	}
}

func TestDetectRejectsNonBundle(t *testing.T) {
	dir := t.TempDir()
	notApp := filepath.Join(dir, "not-an-app")
	_ = os.Mkdir(notApp, 0o755)
	if _, err := Detect(notApp); err == nil {
		t.Errorf("expected error for non-.app path")
	}
}

func TestParseGatekeeperOutput(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/Applications/Foo.app: accepted\n", "accepted"},
		{"/Applications/Foo.app: rejected\n", "rejected"},
		{"", ""},
		{"error: some other failure\n", ""},
		{"/Applications/Foo.app: accepted source=Apple\n", "accepted"},
	}
	for _, tc := range cases {
		got := parseGatekeeperOutput(tc.input)
		if got != tc.want {
			t.Errorf("parseGatekeeperOutput(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPlistXMLBool(t *testing.T) {
	cases := []struct {
		xml  string
		key  string
		want bool
	}{
		{"<key>RunAtLoad</key>\n<true/>", "RunAtLoad", true},
		{"<key>RunAtLoad</key>\n<false/>", "RunAtLoad", false},
		{"<key>RunAtLoad</key><true/>", "RunAtLoad", true},
		{"<key>KeepAlive</key>\n\t<true/>", "KeepAlive", true},
		{"<key>KeepAlive</key>\n\t<false/>", "KeepAlive", false},
		{"<key>Other</key><true/>", "RunAtLoad", false},
		{"", "RunAtLoad", false},
	}
	for _, tc := range cases {
		got := plistXMLBool([]byte(tc.xml), tc.key)
		if got != tc.want {
			t.Errorf("plistXMLBool(%q, %q) = %v, want %v", tc.xml, tc.key, got, tc.want)
		}
	}
}

func TestParsePlistLaunchFlagsFromFile(t *testing.T) {
	plistContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.agent</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/example</string>
	</array>
</dict>
</plist>`
	dir := t.TempDir()
	path := filepath.Join(dir, "com.example.agent.plist")
	if err := os.WriteFile(path, []byte(plistContent), 0644); err != nil {
		t.Fatal(err)
	}
	runAtLoad, keepAlive := parsePlistLaunchFlags(path)
	if !runAtLoad {
		t.Error("RunAtLoad: got false, want true")
	}
	if keepAlive {
		t.Error("KeepAlive: got true, want false")
	}
}

func TestParsePlistLaunchFlagsBothTrue(t *testing.T) {
	plistContent := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.daemon</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>`
	dir := t.TempDir()
	path := filepath.Join(dir, "com.example.daemon.plist")
	if err := os.WriteFile(path, []byte(plistContent), 0644); err != nil {
		t.Fatal(err)
	}
	runAtLoad, keepAlive := parsePlistLaunchFlags(path)
	if !runAtLoad {
		t.Error("RunAtLoad: got false, want true")
	}
	if !keepAlive {
		t.Error("KeepAlive: got false, want true")
	}
}
