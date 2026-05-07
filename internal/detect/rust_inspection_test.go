package detect

import (
	"path/filepath"
	"reflect"
	"testing"
)

type fakeRustMarkers map[string]binaryMarkers

func (f fakeRustMarkers) scanMarkers(path string) binaryMarkers {
	return f[path]
}

type fakeRustLinks map[string][]string

func (f fakeRustLinks) linkedLibs(path string) []string {
	return f[path]
}

type fakeRustSidecars map[string][]string

func (f fakeRustSidecars) sidecars(path string, _ []string) []string {
	return f[path]
}

func TestRustInspectorNativeApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Rusty.app")
	exe := filepath.Join(app, "Contents", "MacOS", "Rusty")
	r := &Result{UI: "Native (Rust)", Runtime: "Rust"}
	inspector := newRustInspector(
		fakeRustMarkers{exe: {rustHits: 384}},
		fakeRustLinks{exe: {
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/Metal.framework/Versions/A/Metal",
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
		}},
		nil,
	)

	got := inspector.inspect(app, exe, r)
	if got == nil {
		t.Fatal("inspect returned nil")
	}
	if got.Kind != "native" {
		t.Fatalf("Kind = %q, want native", got.Kind)
	}
	if got.PrimaryBinary != filepath.Join("Contents", "MacOS", "Rusty") {
		t.Fatalf("PrimaryBinary = %q", got.PrimaryBinary)
	}
	if got.PanicStringHits != 384 {
		t.Fatalf("PanicStringHits = %d, want 384", got.PanicStringHits)
	}
	if want := []string{"AppKit", "Metal"}; !reflect.DeepEqual(got.LinkedFrameworks, want) {
		t.Fatalf("LinkedFrameworks = %#v, want %#v", got.LinkedFrameworks, want)
	}
	if want := []string{"inspect code signing", "inspect entitlements", "inspect helpers", "inspect storage", "inspect network endpoints"}; !reflect.DeepEqual(got.FollowUps, want) {
		t.Fatalf("FollowUps = %#v, want %#v", got.FollowUps, want)
	}
}

func TestRustInspectorTauriApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Tauri.app")
	exe := filepath.Join(app, "Contents", "MacOS", "Tauri")
	r := &Result{UI: "Tauri", Runtime: "Rust"}
	inspector := newRustInspector(
		fakeRustMarkers{exe: {rustHits: 204}},
		fakeRustLinks{exe: {
			"/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit",
			"/System/Library/Frameworks/WebKit.framework/Versions/A/WebKit",
		}},
		nil,
	)

	got := inspector.inspect(app, exe, r)
	if got == nil {
		t.Fatal("inspect returned nil")
	}
	if got.Kind != "tauri" {
		t.Fatalf("Kind = %q, want tauri", got.Kind)
	}
	if want := []string{"AppKit", "WebKit"}; !reflect.DeepEqual(got.LinkedFrameworks, want) {
		t.Fatalf("LinkedFrameworks = %#v, want %#v", got.LinkedFrameworks, want)
	}
	if want := []string{"inspect WebKit usage", "inspect custom protocols", "inspect local resources", "inspect network endpoints"}; !reflect.DeepEqual(got.FollowUps, want) {
		t.Fatalf("FollowUps = %#v, want %#v", got.FollowUps, want)
	}
}

func TestRustInspectorElectronNativeModulesAndSidecars(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Electron.app")
	modPath := filepath.Join("Contents", "Resources", "app.asar.unpacked", "node_modules", "native", "binding.node")
	absMod := filepath.Join(app, modPath)
	sidecar := filepath.Join(app, "Contents", "Resources", "app.asar.unpacked", "node_modules", "native", "libnative.dylib")
	r := &Result{
		UI:      "Electron",
		Runtime: "Node+Chromium",
		NativeModules: []NativeModule{
			{Path: modPath, Language: "Rust"},
			{Path: filepath.Join("Contents", "Resources", "app.asar.unpacked", "node_modules", "sqlite", "sqlite.node"), Language: "C++"},
			{Path: modPath, Language: "Rust"},
		},
	}
	inspector := newRustInspector(
		fakeRustMarkers{sidecar: {rustHits: 88}},
		fakeRustLinks{absMod: {"@rpath/libnative.dylib"}},
		fakeRustSidecars{absMod: {sidecar}},
	)

	got := inspector.inspect(app, "", r)
	if got == nil {
		t.Fatal("inspect returned nil")
	}
	if got.Kind != "electron-native-module" {
		t.Fatalf("Kind = %q, want electron-native-module", got.Kind)
	}
	if want := []string{modPath}; !reflect.DeepEqual(got.NativeModules, want) {
		t.Fatalf("NativeModules = %#v, want %#v", got.NativeModules, want)
	}
	if want := []string{filepath.Join("Contents", "Resources", "app.asar.unpacked", "node_modules", "native", "libnative.dylib")}; !reflect.DeepEqual(got.Sidecars, want) {
		t.Fatalf("Sidecars = %#v, want %#v", got.Sidecars, want)
	}
}

func TestRustInspectorIgnoresNonRustApp(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Plain.app")
	exe := filepath.Join(app, "Contents", "MacOS", "Plain")
	r := &Result{UI: "AppKit", Runtime: "ObjC"}

	got := newRustInspector(fakeRustMarkers{exe: {rustHits: 12}}, nil, nil).inspect(app, exe, r)
	if got != nil {
		t.Fatalf("inspect = %#v, want nil", got)
	}
}
