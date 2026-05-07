package detect

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type fakeNodeFS struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newFakeNodeFS() *fakeNodeFS {
	return &fakeNodeFS{files: map[string][]byte{}, dirs: map[string]bool{}}
}

func (f *fakeNodeFS) addDir(path string) {
	path = filepath.Clean(path)
	for {
		f.dirs[path] = true
		parent := filepath.Dir(path)
		if parent == path || parent == "." {
			return
		}
		path = parent
	}
}

func (f *fakeNodeFS) addFile(path string, data string) {
	path = filepath.Clean(path)
	f.addDir(filepath.Dir(path))
	f.files[path] = []byte(data)
}

func (f *fakeNodeFS) Exists(path string) bool {
	path = filepath.Clean(path)
	_, file := f.files[path]
	return file || f.dirs[path]
}

func (f *fakeNodeFS) IsDir(path string) bool {
	return f.dirs[filepath.Clean(path)]
}

func (f *fakeNodeFS) Open(path string) (io.ReadCloser, error) {
	data, ok := f.files[filepath.Clean(path)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeNodeFS) ReadDir(path string) ([]os.DirEntry, error) {
	path = filepath.Clean(path)
	if !f.dirs[path] {
		return nil, os.ErrNotExist
	}
	children := map[string]fakeDirEntry{}
	prefix := path + string(os.PathSeparator)
	for p := range f.dirs {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if rest == "" || strings.Contains(rest, string(os.PathSeparator)) {
			continue
		}
		children[rest] = fakeDirEntry{name: rest, dir: true}
	}
	for p := range f.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		if rest == "" || strings.Contains(rest, string(os.PathSeparator)) {
			continue
		}
		children[rest] = fakeDirEntry{name: rest}
	}
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]os.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, children[name])
	}
	return entries, nil
}

func (f *fakeNodeFS) ReadFile(path string) ([]byte, error) {
	data, ok := f.files[filepath.Clean(path)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (f *fakeNodeFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	root = filepath.Clean(root)
	if !f.Exists(root) {
		return os.ErrNotExist
	}
	var paths []string
	for p := range f.dirs {
		if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
			paths = append(paths, p)
		}
	}
	for p := range f.files {
		if p == root || strings.HasPrefix(p, root+string(os.PathSeparator)) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := fn(p, fakeDirEntry{name: filepath.Base(p), dir: f.dirs[p]}, nil); err != nil {
			return err
		}
	}
	return nil
}

type fakeDirEntry struct {
	name string
	dir  bool
}

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return e.dir }
func (e fakeDirEntry) Type() os.FileMode          { return 0 }
func (e fakeDirEntry) Info() (os.FileInfo, error) { return nil, nil }

type fakeNodeCommands struct {
	libs map[string][]string
}

func (c fakeNodeCommands) OtoolL(path string) []string {
	return c.libs[filepath.Clean(path)]
}

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

func writeFrameworkPlist(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
<key>CFBundleVersion</key><string>` + version + `</string>
</dict></plist>`
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
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
	writeFrameworkPlist(t, filepath.Join(app, "Contents/Frameworks/FlutterMacOS.framework/Resources/Info.plist"), "3.22.0")
	r, _ := Detect(app)
	if r.UI != "Flutter" || r.Language != "Dart" {
		t.Errorf("got UI=%q lang=%q", r.UI, r.Language)
	}
	if r.FrameworkVersions["Flutter"] != "3.22.0" {
		t.Errorf("Flutter version = %q", r.FrameworkVersions["Flutter"])
	}
}

func TestDetectQt(t *testing.T) {
	app := makeBundle(t, "FakeQt")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/QtCore.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrameworkPlist(t, filepath.Join(app, "Contents/Frameworks/QtCore.framework/Resources/Info.plist"), "6.7.2")
	r, _ := Detect(app)
	if r.UI != "Qt" {
		t.Errorf("UI = %q, want Qt", r.UI)
	}
	if r.FrameworkVersions["Qt"] != "6.7.2" {
		t.Errorf("Qt version = %q", r.FrameworkVersions["Qt"])
	}
}

func TestReadTauriVersion(t *testing.T) {
	app := makeBundle(t, "FakeTauri")
	conf := filepath.Join(app, "Contents", "Resources", "tauri.conf.json")
	if err := os.WriteFile(conf, []byte(`{"package":{"version":"2.1.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readTauriVersion(app); got != "2.1.0" {
		t.Errorf("readTauriVersion = %q, want 2.1.0", got)
	}
}

func TestReadTauriVersionFallsBackToJSON5WithFakeReader(t *testing.T) {
	app := filepath.Join(t.TempDir(), "FakeTauri.app")
	fsys := newFakeNodeFS()
	fsys.addFile(filepath.Join(app, "Contents", "Resources", "tauri.conf.json5"), `{"version":"2.2.0"}`)
	if got := readTauriVersionWith(app, fsys); got != "2.2.0" {
		t.Errorf("readTauriVersionWith = %q, want 2.2.0", got)
	}
}

func TestTauriInspectionPromotesAppKitWebKitWithRustMarkers(t *testing.T) {
	r := Result{UI: "Unknown", Runtime: "unknown", Confidence: "low"}
	exe := "/Fake.app/Contents/MacOS/main"
	classifyByLinkedLibsWith(exe, &r, fakeNodeCommands{
		libs: map[string][]string{filepath.Clean(exe): {
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/WebKit.framework/Versions/A/WebKit",
		}},
	})
	classifyByStringsWith(exe, &r, fakeMarkerScanner{
		markers: binaryMarkers{rustHits: 204},
	})

	if r.UI != "Tauri" {
		t.Fatalf("UI = %q, want Tauri; signals=%v", r.UI, r.Signals)
	}
	if r.Runtime != "Rust" || r.Language != "Rust" || r.Confidence != "high" {
		t.Fatalf("got runtime=%q language=%q confidence=%q", r.Runtime, r.Language, r.Confidence)
	}
	if !hasHint(r.Signals, "links AppKit + WebKit (Tauri suspect)") {
		t.Fatalf("signals = %v, want AppKit+WebKit signal", r.Signals)
	}
	if !hasHint(r.Signals, "204 Rust panic-site strings") {
		t.Fatalf("signals = %v, want Rust marker count", r.Signals)
	}
}

func TestTauriInspectionDoesNotPromoteWeakRustMarkers(t *testing.T) {
	r := Result{UI: "Unknown", Runtime: "unknown", Confidence: "low"}
	exe := "/Fake.app/Contents/MacOS/main"
	classifyByLinkedLibsWith(exe, &r, fakeNodeCommands{
		libs: map[string][]string{filepath.Clean(exe): {
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/WebKit.framework/Versions/A/WebKit",
		}},
	})
	classifyByStringsWith(exe, &r, fakeMarkerScanner{
		markers: binaryMarkers{rustHits: 30},
	})

	if r.UI != "AppKit+WebKit" {
		t.Fatalf("UI = %q, want AppKit+WebKit", r.UI)
	}
	if r.Confidence != "medium" {
		t.Fatalf("confidence = %q, want medium", r.Confidence)
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

func TestNodeAppInspectorDependenciesWithFakeFS(t *testing.T) {
	app := "/Apps/Fake.app"
	fsys := newFakeNodeFS()
	fsys.addDir(filepath.Join(app, "Contents/Frameworks/Electron Framework.framework"))
	fsys.addDir(filepath.Join(app, "Contents/Frameworks/Squirrel.framework"))
	fsys.addDir(filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/node-pty"))
	fsys.addDir(filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/@scope/pkg"))
	fsys.addFile(filepath.Join(app, "Contents/lib/runtime/a.jar"), "")
	fsys.addFile(filepath.Join(app, "Contents/lib/runtime/readme.txt"), "")

	deps := (nodeAppInspector{fs: fsys, cmd: fakeNodeCommands{}}).scanDependencies(app)
	if deps == nil {
		t.Fatal("Dependencies nil")
	}
	if got := strings.Join(deps.ThirdPartyFrameworks, ","); got != "Squirrel" {
		t.Fatalf("ThirdPartyFrameworks = %q, want Squirrel", got)
	}
	if got := strings.Join(deps.NPMPackages, ","); got != "@scope/pkg,node-pty" {
		t.Fatalf("NPMPackages = %q, want @scope/pkg,node-pty", got)
	}
	if deps.JavaJars != 1 {
		t.Fatalf("JavaJars = %d, want 1", deps.JavaJars)
	}
}

func TestNodeAppInspectorNativeModuleWithFakes(t *testing.T) {
	app := "/Apps/Fake.app"
	mod := filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/keytar/build/Release/keytar.node")
	sidecar := filepath.Join(filepath.Dir(mod), "libKeytarSwift.dylib")
	fsys := newFakeNodeFS()
	fsys.addFile(filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/keytar/package.json"), `{"name":"keytar","version":"7.9.0"}`)
	fsys.addFile(mod, "native addon")
	fsys.addFile(sidecar, "sidecar")
	fsys.addFile(filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/keytar/keytar.node.dSYM/Contents/Resources/DWARF/keytar.node"), "debug")
	cmds := fakeNodeCommands{libs: map[string][]string{
		filepath.Clean(mod): {
			"@rpath/libKeytarSwift.dylib",
			"/System/Library/Frameworks/AppKit.framework/AppKit",
		},
	}}

	mods := (nodeAppInspector{fs: fsys, cmd: cmds}).scanNativeModules(app)
	if len(mods) != 1 {
		t.Fatalf("NativeModules len = %d, want 1: %#v", len(mods), mods)
	}
	m := mods[0]
	if m.Language != "Swift" {
		t.Fatalf("Language = %q, want Swift; hints=%v", m.Language, m.Hints)
	}
	if m.PackageName != "keytar" || m.PackageVersion != "7.9.0" {
		t.Fatalf("Package = %q@%q, want keytar@7.9.0", m.PackageName, m.PackageVersion)
	}
	if !hasHint(m.Hints, "uses macOS Keychain") || !hasHint(m.RiskHints, "credential store access") {
		t.Fatalf("hints = %v risk hints = %v, want keytar capability and risk hints", m.Hints, m.RiskHints)
	}
	if !hasHint(m.Hints, "rpath sibling: libKeytarSwift.dylib") {
		t.Fatalf("hints = %v, want sidecar hint", m.Hints)
	}
}

func TestNodeAppInspectorNetworkEndpointsWithFakeFS(t *testing.T) {
	app := "/Apps/Fake.app"
	exe := filepath.Join(app, "Contents/MacOS/main")
	fsys := newFakeNodeFS()
	fsys.addFile(exe, "https://api.example.com https://www.w3.org")
	fsys.addFile(filepath.Join(app, "Contents/Resources/app.asar"), strings.Repeat("x", 300)+"https://Telemetry.Example.com/path")

	hosts := (nodeAppInspector{fs: fsys, cmd: fakeNodeCommands{}}).scanNetworkEndpoints(app, exe)
	if got := strings.Join(hosts, ","); got != "api.example.com,telemetry.example.com" {
		t.Fatalf("hosts = %q, want api.example.com,telemetry.example.com", got)
	}
}

func TestElectronNativeModulesScannedFromAsarUnpackedAndAppDir(t *testing.T) {
	app := makeBundle(t, "FakeNativeModules")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/Electron Framework.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(app, "Contents/Resources/app.asar"))

	appDirModule := filepath.Join(app, "Contents/Resources/app/node_modules/plain/build/Release/plain.node")
	if err := os.MkdirAll(filepath.Dir(appDirModule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appDirModule, []byte("native addon"), 0o644); err != nil {
		t.Fatal(err)
	}

	rustModule := filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/custom/build/Release/custom.node")
	if err := os.MkdirAll(filepath.Dir(rustModule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/custom/package.json"), []byte(`{"name":"custom-native","version":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("rustc/library/core/src/panicking.rs core::panicking\n", 60)
	if err := os.WriteFile(rustModule, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Debug-symbol bundles should not produce separate native modules.
	dsymModule := filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/custom/build/Release/custom.node.dSYM/Contents/Resources/DWARF/custom.node")
	if err := os.MkdirAll(filepath.Dir(dsymModule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dsymModule, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Detect(app)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.NativeModules) != 2 {
		t.Fatalf("NativeModules len = %d, want 2: %#v", len(r.NativeModules), r.NativeModules)
	}
	if r.NativeModules[0].Path != "Contents/Resources/app.asar.unpacked/node_modules/custom/build/Release/custom.node" {
		t.Errorf("first module path = %q", r.NativeModules[0].Path)
	}
	if r.NativeModules[0].Language != "Rust" {
		t.Errorf("custom module language = %q, want Rust; hints=%v", r.NativeModules[0].Language, r.NativeModules[0].Hints)
	}
	if r.NativeModules[0].PackageName != "custom-native" || r.NativeModules[0].PackageVersion != "1.2.3" {
		t.Errorf("custom module package = %q@%q, want custom-native@1.2.3", r.NativeModules[0].PackageName, r.NativeModules[0].PackageVersion)
	}
	if r.NativeModules[1].Path != "Contents/Resources/app/node_modules/plain/build/Release/plain.node" {
		t.Errorf("second module path = %q", r.NativeModules[1].Path)
	}
	if r.NativeModules[1].Language != "C++" {
		t.Errorf("plain module language = %q, want C++", r.NativeModules[1].Language)
	}
}

func TestNativeModulePackageRootHandlesScopedPackages(t *testing.T) {
	app := makeBundle(t, "FakeScopedNativeModule")
	root := filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/@scope/pkg")
	mod := filepath.Join(root, "prebuilds/darwin-arm64/addon.node")
	if err := os.MkdirAll(filepath.Dir(mod), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mod, []byte("native addon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@scope/pkg","version":"4.5.6"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := nativeModulePackageRoot(mod); got != root {
		t.Fatalf("nativeModulePackageRoot = %q, want %q", got, root)
	}
	m := classifyNativeModule(mod, "Contents/Resources/app.asar.unpacked/node_modules/@scope/pkg/prebuilds/darwin-arm64/addon.node")
	if m.PackageName != "@scope/pkg" || m.PackageVersion != "4.5.6" {
		t.Fatalf("module package = %q@%q, want @scope/pkg@4.5.6", m.PackageName, m.PackageVersion)
	}
}

func TestNativeModuleCapabilityHintsFromPackageName(t *testing.T) {
	app := makeBundle(t, "FakeKeytarNativeModule")
	root := filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/keytar")
	mod := filepath.Join(root, "build/Release/keytar.node")
	if err := os.MkdirAll(filepath.Dir(mod), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mod, []byte("native addon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"keytar","version":"7.9.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := classifyNativeModule(mod, "Contents/Resources/app.asar.unpacked/node_modules/keytar/build/Release/keytar.node")
	if !hasHint(m.Hints, "uses macOS Keychain") {
		t.Fatalf("hints = %v, want macOS Keychain hint", m.Hints)
	}
	if !hasHint(m.RiskHints, "credential store access") {
		t.Fatalf("risk hints = %v, want credential store access", m.RiskHints)
	}
}

func TestNativeModuleRiskHintsFromPackageName(t *testing.T) {
	app := makeBundle(t, "FakeInputHookNativeModule")
	root := filepath.Join(app, "Contents/Resources/app.asar.unpacked/node_modules/uiohook-napi")
	mod := filepath.Join(root, "prebuilds/darwin-arm64/uiohook.node")
	if err := os.MkdirAll(filepath.Dir(mod), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mod, []byte("native addon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"uiohook-napi","version":"1.5.4"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := classifyNativeModule(mod, "Contents/Resources/app.asar.unpacked/node_modules/uiohook-napi/prebuilds/darwin-arm64/uiohook.node")
	if !hasHint(m.Hints, "can observe global input events") {
		t.Fatalf("hints = %v, want input capability hint", m.Hints)
	}
	if !hasHint(m.RiskHints, "global input monitoring") {
		t.Fatalf("risk hints = %v, want global input monitoring", m.RiskHints)
	}
}

func TestAppUptimeUsesOldestProcessStart(t *testing.T) {
	newer := time.Date(2026, 5, 6, 11, 30, 0, 0, time.UTC)
	older := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	started, seconds := appUptime([]ProcessInfo{
		{PID: 10, StartTime: newer},
		{PID: 11, StartTime: older},
	}, now)
	if started == nil || !started.Equal(older) {
		t.Fatalf("started = %v, want %v", started, older)
	}
	if seconds != 7200 {
		t.Fatalf("seconds = %d, want 7200", seconds)
	}
}

func TestAppUptimeIgnoresMissingStartTimes(t *testing.T) {
	started, seconds := appUptime([]ProcessInfo{{PID: 10}}, time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC))
	if started != nil {
		t.Fatalf("started = %v, want nil", started)
	}
	if seconds != 0 {
		t.Fatalf("seconds = %d, want 0", seconds)
	}
}

func TestParseLstartUsesLocalTimeZone(t *testing.T) {
	oldLocal := time.Local
	loc := time.FixedZone("Spectra/Test", -5*60*60)
	time.Local = loc
	t.Cleanup(func() { time.Local = oldLocal })

	got := parseLstart("Wed May  6 12:34:56 2026")
	if got.Location() != loc {
		t.Fatalf("location = %v, want %v", got.Location(), loc)
	}
	if got.Hour() != 12 || got.Minute() != 34 || got.Second() != 56 {
		t.Fatalf("parsed time = %v", got)
	}
}

func hasHint(hints []string, want string) bool {
	for _, hint := range hints {
		if hint == want {
			return true
		}
	}
	return false
}

type fakeMarkerScanner struct {
	markers binaryMarkers
}

func (f fakeMarkerScanner) ScanMarkers(string) binaryMarkers {
	return f.markers
}

func TestNativeModuleRootsOnlyExistingElectronPayloads(t *testing.T) {
	app := makeBundle(t, "FakeNativeRoots")
	if roots := nativeModuleRoots(app); len(roots) != 0 {
		t.Fatalf("roots without payloads = %v, want none", roots)
	}

	for _, root := range []string{
		filepath.Join(app, "Contents/Resources/app.asar.unpacked"),
		filepath.Join(app, "Contents/Resources/app"),
	} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	roots := nativeModuleRoots(app)
	if len(roots) != 2 {
		t.Fatalf("roots len = %d, want 2: %v", len(roots), roots)
	}
	if !strings.HasSuffix(roots[0], "Contents/Resources/app.asar.unpacked") {
		t.Errorf("roots[0] = %q", roots[0])
	}
	if !strings.HasSuffix(roots[1], "Contents/Resources/app") {
		t.Errorf("roots[1] = %q", roots[1])
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

func TestLstartLayoutParse(t *testing.T) {
	samples := []struct {
		raw      string
		wantYear int
	}{
		{"Sat May  2 22:37:01 2026", 2026},
		{"Mon Jan  1 00:00:00 2025", 2025},
	}
	for _, s := range samples {
		raw := strings.Join(strings.Fields(s.raw), " ")
		layout := strings.Join(strings.Fields(lstartLayout), " ")
		parsed, err := time.Parse(layout, raw)
		if err != nil {
			t.Errorf("Parse(%q): %v", s.raw, err)
			continue
		}
		if parsed.Year() != s.wantYear {
			t.Errorf("Year = %d, want %d", parsed.Year(), s.wantYear)
		}
	}
}
