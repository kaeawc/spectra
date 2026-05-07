package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakePythonCommandRunner struct {
	outputs map[string][]byte
	errs    map[string]error
	calls   []string
}

func (f *fakePythonCommandRunner) Run(name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if err := f.errs[call]; err != nil {
		return nil, err
	}
	return f.outputs[call], nil
}

func TestPythonAppScannerPy2AppPackagesAndBundledFramework(t *testing.T) {
	app := makeBundle(t, "FakePy2App")
	touch(t, filepath.Join(app, "Contents/Resources/__boot__.py"))
	touch(t, filepath.Join(app, "Contents/Frameworks/Python.framework/Python"))
	writeFile(t, filepath.Join(app, "Contents/Frameworks/Python.framework/Resources/Info.plist"), `<plist><dict><key>CFBundleShortVersionString</key><string>3.12.4</string></dict></plist>`)
	writeFile(t, filepath.Join(app, "Contents/Resources/lib/python3.12/site-packages/requests-2.32.3.dist-info/METADATA"), "Name: requests\nVersion: 2.32.3\n")

	got := pythonAppScanner{fs: osPythonAppFS{}, commands: &fakePythonCommandRunner{}}.Scan(app)
	if got == nil {
		t.Fatal("PythonApp nil")
	}
	if got.Packaging != "py2app" {
		t.Fatalf("Packaging = %q, want py2app", got.Packaging)
	}
	if got.RuntimeSource != "bundled-framework" {
		t.Fatalf("RuntimeSource = %q, want bundled-framework", got.RuntimeSource)
	}
	if got.RuntimePath != "Contents/Frameworks/Python.framework" {
		t.Fatalf("RuntimePath = %q", got.RuntimePath)
	}
	if got.Version != "3.12.4" {
		t.Fatalf("Version = %q, want 3.12.4", got.Version)
	}
	if len(got.Packages) != 1 || got.Packages[0].Name != "requests" || got.Packages[0].Version != "2.32.3" {
		t.Fatalf("Packages = %#v, want requests 2.32.3", got.Packages)
	}
}

func TestPythonAppScannerPyInstallerNativeExtensionHints(t *testing.T) {
	app := makeBundle(t, "FakePyInstaller")
	touch(t, filepath.Join(app, "Contents/Resources/base_library.zip"))
	ext := filepath.Join(app, "Contents/Resources/lib/python3.11/site-packages/capture/_capture.so")
	writeFile(t, ext, "native extension")
	runner := &fakePythonCommandRunner{outputs: map[string][]byte{
		"otool -L " + ext: []byte(ext + ":\n\t/System/Library/Frameworks/ScreenCaptureKit.framework/Versions/A/ScreenCaptureKit (compatibility version 1.0.0, current version 1.0.0)\n\t/usr/lib/libc++.1.dylib (compatibility version 1.0.0, current version 1700.255.0)\n"),
	}}

	got := pythonAppScanner{fs: osPythonAppFS{}, commands: runner}.Scan(app)
	if got == nil {
		t.Fatal("PythonApp nil")
	}
	if got.Packaging != "pyinstaller" {
		t.Fatalf("Packaging = %q, want pyinstaller", got.Packaging)
	}
	if len(got.NativeExtensions) != 1 {
		t.Fatalf("NativeExtensions = %#v, want one extension", got.NativeExtensions)
	}
	extGot := got.NativeExtensions[0]
	if !hasHint(extGot.Hints, "uses ScreenCaptureKit") {
		t.Fatalf("Hints = %v, want ScreenCaptureKit", extGot.Hints)
	}
	if !hasHint(extGot.RiskHints, "screen capture capability") {
		t.Fatalf("RiskHints = %v, want screen capture capability", extGot.RiskHints)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("otool calls = %v, want one call", runner.calls)
	}
}

func TestPythonAppScannerExternalInterpreterAndEnvironmentRisk(t *testing.T) {
	app := makeBundle(t, "FakeCustomPython")
	launcher := filepath.Join(app, "Contents/MacOS/main")
	writeFile(t, launcher, "#!/bin/sh\nPYTHONPATH=\"$HOME/dev/pkg\" exec /opt/homebrew/bin/python3 \"$@\"\n")

	got := pythonAppScanner{fs: osPythonAppFS{}, commands: &fakePythonCommandRunner{}}.Scan(app)
	if got == nil {
		t.Fatal("PythonApp nil")
	}
	if got.Packaging != "custom" {
		t.Fatalf("Packaging = %q, want custom", got.Packaging)
	}
	if got.RuntimeSource != "brew" || got.RuntimePath != "/opt/homebrew/bin/python3" {
		t.Fatalf("runtime = %q %q, want brew /opt/homebrew/bin/python3", got.RuntimeSource, got.RuntimePath)
	}
	if !hasHint(got.RiskHints, "external interpreter path") {
		t.Fatalf("RiskHints = %v, want external interpreter path", got.RiskHints)
	}
	if !hasHint(got.RiskHints, "environment-sensitive launcher") {
		t.Fatalf("RiskHints = %v, want environment-sensitive launcher", got.RiskHints)
	}
}

func TestDetectPopulatesPythonAppWithoutOverridingQt(t *testing.T) {
	app := makeBundle(t, "FakeQtPython")
	if err := os.MkdirAll(filepath.Join(app, "Contents/Frameworks/QtCore.framework"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app, "Contents/Resources/app_packages"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(app, "Contents/Resources/app/main.py"), "print('hello')\n")
	writeFile(t, filepath.Join(app, "Contents/Resources/app_packages/numpy-2.0.0.dist-info/METADATA"), "Name: numpy\nVersion: 2.0.0\n")

	r, err := Detect(app)
	if err != nil {
		t.Fatal(err)
	}
	if r.UI != "Qt" {
		t.Fatalf("UI = %q, want Qt", r.UI)
	}
	if r.PythonApp == nil {
		t.Fatal("PythonApp nil")
	}
	if r.PythonApp.Packaging != "briefcase" {
		t.Fatalf("Packaging = %q, want briefcase", r.PythonApp.Packaging)
	}
	if len(r.PythonApp.Packages) != 1 || r.PythonApp.Packages[0].Name != "numpy" {
		t.Fatalf("Packages = %#v, want numpy", r.PythonApp.Packages)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPythonRuntimeSourceForPath(t *testing.T) {
	tests := map[string]string{
		"/usr/bin/python3":                                             "system",
		"/opt/homebrew/bin/python3":                                    "brew",
		"/Users/me/.pyenv/versions/3.12.1/bin/python":                  "pyenv",
		"/Users/me/.local/share/uv/python/3.12.1/bin/python3":          "uv",
		"/Users/me/.local/share/mise/installs/python/3.12/bin/python3": "mise",
		"/Users/me/.asdf/installs/python/3.11.9/bin/python3":           "asdf",
	}
	for path, want := range tests {
		t.Run(fmt.Sprintf("%s=>%s", path, want), func(t *testing.T) {
			if got := pythonRuntimeSourceForPath(path); got != want {
				t.Fatalf("pythonRuntimeSourceForPath(%q) = %q, want %q", path, got, want)
			}
		})
	}
}
