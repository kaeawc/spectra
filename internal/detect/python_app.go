package detect

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var errStopPythonWalk = errors.New("stop python app walk")

type pythonAppFS interface {
	Stat(name string) (fs.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
}

type pythonCommandRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

type osPythonAppFS struct{}

func (osPythonAppFS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (osPythonAppFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (osPythonAppFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}

type execPythonCommandRunner struct{}

func (execPythonCommandRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type pythonAppScanner struct {
	fs       pythonAppFS
	commands pythonCommandRunner
}

func scanPythonApp(appPath string) *PythonApp {
	return pythonAppScanner{
		fs:       osPythonAppFS{},
		commands: execPythonCommandRunner{},
	}.Scan(appPath)
}

func (s pythonAppScanner) Scan(appPath string) *PythonApp {
	if s.fs == nil {
		s.fs = osPythonAppFS{}
	}
	if s.commands == nil {
		s.commands = execPythonCommandRunner{}
	}

	contents := filepath.Join(appPath, "Contents")
	resources := filepath.Join(contents, "Resources")
	app := &PythonApp{
		Packaging:     s.detectPythonPackaging(contents, resources),
		RuntimeSource: "unknown",
	}
	app.RuntimeSource, app.RuntimePath = s.resolvePythonRuntime(appPath)
	app.Version = s.detectPythonVersion(appPath, app.RuntimePath)
	app.ModuleRoots = s.pythonModuleRoots(appPath)
	app.Packages = s.pythonPackages(appPath)
	app.NativeExtensions = s.pythonNativeExtensions(appPath)
	app.RiskHints = s.pythonRiskHints(appPath, app)

	if app.Packaging == "unknown" && app.RuntimePath == "" && len(app.ModuleRoots) == 0 && len(app.Packages) == 0 && len(app.NativeExtensions) == 0 {
		return nil
	}
	return app
}

func (s pythonAppScanner) detectPythonPackaging(contents, resources string) string {
	switch {
	case s.exists(filepath.Join(resources, "__boot__.py")) || s.exists(filepath.Join(resources, "python-libraries.zip")):
		return "py2app"
	case s.exists(filepath.Join(resources, "base_library.zip")) || s.exists(filepath.Join(resources, "lib-dynload")) || s.hasGlob(resources, "PYZ-*"):
		return "pyinstaller"
	case s.exists(filepath.Join(resources, "app_packages")) && s.exists(filepath.Join(resources, "app")):
		return "briefcase"
	case s.hasNuitkaMarker(contents):
		return "nuitka"
	case s.launcherReferencesPython(contents):
		return "custom"
	default:
		return "unknown"
	}
}

func (s pythonAppScanner) resolvePythonRuntime(appPath string) (string, string) {
	candidates := []struct {
		source string
		path   string
	}{
		{"bundled-framework", filepath.Join(appPath, "Contents", "Frameworks", "Python.framework")},
		{"bundled-executable", filepath.Join(appPath, "Contents", "Resources", "python", "bin", "python3")},
		{"bundled-executable", filepath.Join(appPath, "Contents", "MacOS", "python3")},
		{"bundled-executable", filepath.Join(appPath, "Contents", "MacOS", "python")},
	}
	for _, c := range candidates {
		if s.exists(c.path) {
			return c.source, relToApp(appPath, c.path)
		}
	}
	if p := s.firstMatchingFile(filepath.Join(appPath, "Contents", "Frameworks"), `libpython*.dylib`); p != "" {
		return "bundled-dylib", relToApp(appPath, p)
	}
	if p := s.externalInterpreterFromLaunchers(appPath); p != "" {
		return pythonRuntimeSourceForPath(p), p
	}
	return "unknown", ""
}

func (s pythonAppScanner) detectPythonVersion(appPath, runtimePath string) string {
	if runtimePath == "" {
		return s.versionFromLayout(appPath)
	}
	if filepath.IsAbs(runtimePath) {
		return s.versionFromLayout(appPath)
	}
	abs := filepath.Join(appPath, runtimePath)
	for _, path := range []string{
		filepath.Join(abs, "Resources", "Info.plist"),
		filepath.Join(abs, "Versions", "Current", "Resources", "Info.plist"),
		filepath.Join(filepath.Dir(filepath.Dir(abs)), "pyvenv.cfg"),
	} {
		if v := s.versionFromFile(path); v != "" {
			return v
		}
	}
	return s.versionFromLayout(appPath)
}

func (s pythonAppScanner) pythonModuleRoots(appPath string) []string {
	candidates := []string{
		filepath.Join(appPath, "Contents", "Resources", "app"),
		filepath.Join(appPath, "Contents", "Resources", "app_packages"),
		filepath.Join(appPath, "Contents", "Resources", "lib", "python"),
		filepath.Join(appPath, "Contents", "Resources", "python"),
	}
	var roots []string
	for _, p := range candidates {
		if s.exists(p) {
			roots = append(roots, relToApp(appPath, p))
		}
	}
	if s.exists(filepath.Join(appPath, "Contents", "Resources", "site.py")) {
		roots = append(roots, filepath.ToSlash(filepath.Join("Contents", "Resources")))
	}
	sort.Strings(roots)
	return dedupePythonStrings(roots)
}

func (s pythonAppScanner) pythonPackages(appPath string) []PythonPackage {
	var packages []PythonPackage
	_ = s.fs.WalkDir(filepath.Join(appPath, "Contents"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if name != "METADATA" && name != "PKG-INFO" {
			return nil
		}
		parent := filepath.Base(filepath.Dir(path))
		if !strings.HasSuffix(parent, ".dist-info") && !strings.HasSuffix(parent, ".egg-info") {
			return nil
		}
		data, readErr := s.fs.ReadFile(path)
		if readErr == nil {
			pkg := parsePythonPackageMetadata(data)
			if pkg.Name != "" {
				pkg.Path = relToApp(appPath, path)
				packages = append(packages, pkg)
			}
		}
		return nil
	})
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Name == packages[j].Name {
			return packages[i].Path < packages[j].Path
		}
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
	return packages
}

func (s pythonAppScanner) pythonNativeExtensions(appPath string) []PythonNativeExtension {
	var exts []PythonNativeExtension
	_ = s.fs.WalkDir(filepath.Join(appPath, "Contents"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".so") {
			return nil
		}
		rel := relToApp(appPath, path)
		ext := PythonNativeExtension{Name: d.Name(), Path: rel}
		if libs, err := s.otoolL(path); err == nil {
			ext.LinkedLibs = libs
			ext.Hints = pythonNativeExtensionHints(libs)
			ext.RiskHints = pythonNativeExtensionRiskHints(libs, d.Name())
		}
		exts = append(exts, ext)
		return nil
	})
	sort.Slice(exts, func(i, j int) bool { return exts[i].Path < exts[j].Path })
	return exts
}

func (s pythonAppScanner) pythonRiskHints(appPath string, app *PythonApp) []string {
	var hints []string
	if app.RuntimePath != "" && filepath.IsAbs(app.RuntimePath) {
		hints = append(hints, "external interpreter path")
	}
	for _, root := range app.ModuleRoots {
		if strings.Contains(root, "../") {
			hints = append(hints, "module root outside app bundle")
			continue
		}
		abs := filepath.Join(appPath, root)
		if info, err := s.fs.Stat(abs); err == nil && info.Mode().Perm()&0o022 != 0 {
			hints = append(hints, "writable module root: "+root)
		}
	}
	if s.launcherContainsAny(appPath, []string{"PYTHONPATH=", "PYTHONUSERBASE=", "DYLD_LIBRARY_PATH=", "DYLD_INSERT_LIBRARIES="}) {
		hints = append(hints, "environment-sensitive launcher")
	}
	return dedupePythonStrings(hints)
}

func (s pythonAppScanner) otoolL(path string) ([]string, error) {
	out, err := s.commands.Run("otool", "-L", path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(out), "\n")
	var libs []string
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			libs = append(libs, fields[0])
		}
	}
	return libs, nil
}

func (s pythonAppScanner) exists(path string) bool {
	_, err := s.fs.Stat(path)
	return err == nil
}

func (s pythonAppScanner) hasGlob(root, pattern string) bool {
	var found bool
	_ = s.fs.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() || found {
			return nil
		}
		ok, _ := filepath.Match(pattern, d.Name())
		if ok {
			found = true
			return errStopPythonWalk
		}
		return nil
	})
	return found
}

func (s pythonAppScanner) firstMatchingFile(root, pattern string) string {
	var found string
	_ = s.fs.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() || found != "" {
			return nil
		}
		ok, _ := filepath.Match(pattern, d.Name())
		if ok {
			found = path
			return errStopPythonWalk
		}
		return nil
	})
	return found
}

func (s pythonAppScanner) hasNuitkaMarker(root string) bool {
	var found bool
	_ = s.fs.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() || found {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.Contains(name, "nuitka") {
			found = true
			return errStopPythonWalk
		}
		if strings.HasSuffix(name, ".so") {
			data, err := s.fs.ReadFile(path)
			if err == nil && bytes.Contains(bytes.ToLower(data), []byte("nuitka")) {
				found = true
				return errStopPythonWalk
			}
		}
		return nil
	})
	return found
}

func (s pythonAppScanner) launcherReferencesPython(contents string) bool {
	return s.launcherContainsAny(filepath.Dir(contents), []string{"python", "Python.framework", "libpython"})
}

func (s pythonAppScanner) launcherContainsAny(appPath string, needles []string) bool {
	macos := filepath.Join(appPath, "Contents", "MacOS")
	var found bool
	_ = s.fs.WalkDir(macos, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() || found {
			return nil
		}
		data, readErr := s.fs.ReadFile(path)
		if readErr == nil {
			for _, needle := range needles {
				if bytes.Contains(data, []byte(needle)) {
					found = true
					return errStopPythonWalk
				}
			}
		}
		return nil
	})
	return found
}

func (s pythonAppScanner) externalInterpreterFromLaunchers(appPath string) string {
	re := regexp.MustCompile(`(/(?:opt/homebrew|usr/local|usr|Users/[^[:space:]"']+)/[^[:space:]"']*python[0-9.]*)`)
	var found string
	_ = s.fs.WalkDir(filepath.Join(appPath, "Contents", "MacOS"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || d.IsDir() || found != "" {
			return nil
		}
		data, readErr := s.fs.ReadFile(path)
		if readErr == nil {
			if m := re.FindSubmatch(data); len(m) > 1 {
				found = string(m[1])
				return errStopPythonWalk
			}
		}
		return nil
	})
	return found
}

func (s pythonAppScanner) versionFromLayout(appPath string) string {
	re := regexp.MustCompile(`python([0-9]+\.[0-9]+)`)
	var version string
	_ = s.fs.WalkDir(filepath.Join(appPath, "Contents"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || version != "" {
			return nil
		}
		if m := re.FindStringSubmatch(d.Name()); len(m) == 2 {
			version = m[1]
			return errStopPythonWalk
		}
		if d.Name() == "pyvenv.cfg" {
			version = s.versionFromFile(path)
			if version != "" {
				return errStopPythonWalk
			}
		}
		return nil
	})
	return version
}

func (s pythonAppScanner) versionFromFile(path string) string {
	data, err := s.fs.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "version") || strings.HasPrefix(lower, "cfbundleshortversionstring") {
			if idx := strings.Index(line, "="); idx >= 0 {
				return strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`)
			}
		}
	}
	re := regexp.MustCompile(`([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
	return re.FindString(string(data))
}

func parsePythonPackageMetadata(data []byte) PythonPackage {
	var pkg PythonPackage
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Name:"):
			pkg.Name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		case strings.HasPrefix(line, "Version:"):
			pkg.Version = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		}
	}
	return pkg
}

func pythonRuntimeSourceForPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/usr/bin/"):
		return "system"
	case strings.HasPrefix(path, "/opt/homebrew/") || strings.HasPrefix(path, "/usr/local/"):
		return "brew"
	case strings.Contains(path, "/.pyenv/"):
		return "pyenv"
	case strings.Contains(path, "/.local/share/uv/"):
		return "uv"
	case strings.Contains(path, "/.local/share/mise/"):
		return "mise"
	case strings.Contains(path, "/.asdf/"):
		return "asdf"
	default:
		return "unknown"
	}
}

func pythonNativeExtensionHints(libs []string) []string {
	var hints []string
	for _, lib := range libs {
		for _, fw := range []string{"AVFoundation", "CoreAudio", "CoreBluetooth", "CoreLocation", "Metal", "Quartz", "ScreenCaptureKit", "Security"} {
			if strings.Contains(lib, "/"+fw+".framework/") {
				hints = append(hints, "uses "+fw)
			}
		}
		if strings.Contains(lib, "libswift") {
			hints = append(hints, "links Swift runtime")
		}
		if strings.Contains(lib, "libc++") {
			hints = append(hints, "links libc++")
		}
		if strings.Contains(lib, "libpython") {
			hints = append(hints, "links CPython runtime")
		}
	}
	return dedupePythonStrings(hints)
}

func pythonNativeExtensionRiskHints(libs []string, name string) []string {
	var hints []string
	lowerName := strings.ToLower(name)
	if strings.Contains(lowerName, "keychain") || strings.Contains(lowerName, "security") {
		hints = append(hints, "credential or security API access")
	}
	for _, lib := range libs {
		switch {
		case strings.Contains(lib, "/ScreenCaptureKit.framework/"):
			hints = append(hints, "screen capture capability")
		case strings.Contains(lib, "/CoreLocation.framework/"):
			hints = append(hints, "location capability")
		case strings.Contains(lib, "/CoreBluetooth.framework/"):
			hints = append(hints, "Bluetooth device access")
		case strings.Contains(lib, "/Security.framework/"):
			hints = append(hints, "credential or security API access")
		}
	}
	return dedupePythonStrings(hints)
}

func relToApp(appPath, path string) string {
	rel, err := filepath.Rel(appPath, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func dedupePythonStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
