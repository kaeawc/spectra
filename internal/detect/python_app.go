package detect

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/proc"
)

// Compiled once at package load — these were previously rebuilt on every call
// (some inside directory walks, i.e. per file).
var (
	externalPythonInterpreterRE = regexp.MustCompile(`(/(?:opt/homebrew|usr/local|usr|Users/[^[:space:]"']+)/[^[:space:]"']*python[0-9.]*)`)
	pythonLayoutVersionRE       = regexp.MustCompile(`python([0-9]+\.[0-9]+)`)
	pythonDottedVersionRE       = regexp.MustCompile(`([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)
)

type pythonAppFS interface {
	Stat(name string) (fs.FileInfo, error)
	ReadFile(name string) ([]byte, error)
	// Open streams a file so large binaries are scanned in chunks instead of
	// being read whole.
	Open(name string) (pythonAppFile, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
}

type pythonAppFile interface {
	io.Reader
	io.ReaderAt
	io.Closer
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

func (osPythonAppFS) Open(name string) (pythonAppFile, error) {
	return os.Open(name)
}

func (osPythonAppFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}

type execPythonCommandRunner struct{}

func (execPythonCommandRunner) Run(name string, args ...string) ([]byte, error) {
	return proc.BoundedOutput(name, args...)
}

type pythonAppScanner struct {
	fs       pythonAppFS
	commands pythonCommandRunner
	// chunk and tokenCap override pythonScanChunk and pythonMaxTokenCarry;
	// tests shrink them to exercise chunk boundaries and the long-token path.
	chunk    int
	tokenCap int
	// tree, when it recorded a complete listing of Contents from the real
	// filesystem, stands in for walking Contents again.
	tree *treeRecord
}

// scanPythonApp scans the bundle; tree is the Contents listing recorded by
// the bundle-size walk, or nil.
func scanPythonApp(appPath string, tree *treeRecord) *PythonApp {
	return pythonAppScanner{
		fs:       osPythonAppFS{},
		commands: execPythonCommandRunner{},
		tree:     tree,
	}.Scan(appPath)
}

// pythonScan is the per-Scan state. Every detection pass reads the bundle
// through it so Contents is walked once and each file is opened at most once:
// walks are recorded and replayed per sub-root, file marker hits and parsed
// version files are cached by path.
type pythonScan struct {
	pythonAppScanner
	contents string
	macos    string
	walks    map[string][]pythonWalkEntry
	markers  map[string]pythonFileMarkers
	versions map[string]string
	buf      []byte
}

// pythonWalkEntry is one recorded WalkDir callback. A non-nil err marks the
// point where an original per-root walk would have aborted.
type pythonWalkEntry struct {
	path string
	d    fs.DirEntry
	err  error
}

func (s pythonAppScanner) Scan(appPath string) *PythonApp {
	if s.fs == nil {
		s.fs = osPythonAppFS{}
	}
	if s.commands == nil {
		s.commands = execPythonCommandRunner{}
	}
	contents := filepath.Join(appPath, "Contents")
	sc := &pythonScan{
		pythonAppScanner: s,
		contents:         contents,
		macos:            filepath.Join(contents, "MacOS"),
		walks:            map[string][]pythonWalkEntry{},
		markers:          map[string]pythonFileMarkers{},
		versions:         map[string]string{},
	}
	return sc.scan(appPath)
}

func (s *pythonScan) scan(appPath string) *PythonApp {
	resources := filepath.Join(s.contents, "Resources")
	app := &PythonApp{
		Packaging:     s.detectPythonPackaging(s.contents, resources),
		RuntimeSource: "unknown",
	}
	app.RuntimeSource, app.RuntimePath = s.resolvePythonRuntime(appPath)
	app.Version = s.detectPythonVersion(appPath, app.RuntimePath)
	app.ModuleRoots = s.pythonModuleRoots(appPath)
	app.Packages, app.NativeExtensions = s.pythonPackagesAndExtensions(appPath)
	app.RiskHints = s.pythonRiskHints(appPath, app)

	if app.Packaging == "unknown" && app.RuntimePath == "" && len(app.ModuleRoots) == 0 && len(app.Packages) == 0 && len(app.NativeExtensions) == 0 {
		return nil
	}
	return app
}

// entries returns the WalkDir callbacks that a walk rooted at root would
// produce, up to (not including) the first error that would abort it. When
// the recorded Contents walk covers root, root's entries are the contiguous
// pre-order block under it; otherwise (Contents missing, a symlink, or
// unreadable) root is walked on its own.
func (s *pythonScan) entries(root string) []pythonWalkEntry {
	if root != s.contents {
		all := s.walk(s.contents)
		if len(all) > 0 && all[0].err == nil && all[0].d != nil && all[0].d.IsDir() &&
			(len(all) < 2 || all[1].err == nil || all[1].path != s.contents) {
			return truncateAtWalkError(subtreeEntries(all, root))
		}
	}
	return truncateAtWalkError(s.walk(root))
}

func (s *pythonScan) walk(root string) []pythonWalkEntry {
	if w, ok := s.walks[root]; ok {
		return w
	}
	if root == s.contents {
		if w, ok := s.tree.walkEntries(root); ok {
			s.walks[root] = w
			return w
		}
	}
	var w []pythonWalkEntry
	_ = s.fs.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		w = append(w, pythonWalkEntry{path: path, d: d, err: err})
		return nil
	})
	s.walks[root] = w
	return w
}

func subtreeEntries(all []pythonWalkEntry, root string) []pythonWalkEntry {
	prefix := root + string(filepath.Separator)
	start := -1
	for i, e := range all {
		in := e.path == root || strings.HasPrefix(e.path, prefix)
		if in && start < 0 {
			start = i
		} else if !in && start >= 0 {
			return all[start:i]
		}
	}
	if start < 0 {
		return nil
	}
	return all[start:]
}

func truncateAtWalkError(entries []pythonWalkEntry) []pythonWalkEntry {
	for i, e := range entries {
		if e.err != nil {
			return entries[:i]
		}
	}
	return entries
}

func (s *pythonScan) detectPythonPackaging(contents, resources string) string {
	switch {
	case s.exists(filepath.Join(resources, "__boot__.py")) || s.exists(filepath.Join(resources, "python-libraries.zip")):
		return "py2app"
	case s.exists(filepath.Join(resources, "base_library.zip")) || s.exists(filepath.Join(resources, "lib-dynload")) || s.hasGlob(resources, "PYZ-*"):
		return "pyinstaller"
	case s.exists(filepath.Join(resources, "app_packages")) && s.exists(filepath.Join(resources, "app")):
		return "briefcase"
	case s.hasNuitkaMarker(contents):
		return "nuitka"
	case s.launcherReferencesPython():
		return "custom"
	default:
		return "unknown"
	}
}

func (s *pythonScan) resolvePythonRuntime(appPath string) (string, string) {
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
	if p := s.externalInterpreterFromLaunchers(); p != "" {
		return pythonRuntimeSourceForPath(p), p
	}
	return "unknown", ""
}

func (s *pythonScan) detectPythonVersion(appPath, runtimePath string) string {
	if runtimePath == "" {
		return s.versionFromLayout()
	}
	if filepath.IsAbs(runtimePath) {
		return s.versionFromLayout()
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
	return s.versionFromLayout()
}

func (s *pythonScan) pythonModuleRoots(appPath string) []string {
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

// pythonPackagesAndExtensions collects installed packages (dist-info/egg-info
// METADATA) and compiled .so native extensions from the Contents walk.
func (s *pythonScan) pythonPackagesAndExtensions(appPath string) ([]PythonPackage, []PythonNativeExtension) {
	var packages []PythonPackage
	var exts []PythonNativeExtension
	for _, e := range s.entries(s.contents) {
		if e.d == nil || e.d.IsDir() {
			continue
		}
		path, name := e.path, e.d.Name()
		switch {
		case name == "METADATA" || name == "PKG-INFO":
			parent := filepath.Base(filepath.Dir(path))
			if !strings.HasSuffix(parent, ".dist-info") && !strings.HasSuffix(parent, ".egg-info") {
				continue
			}
			if data, readErr := s.fs.ReadFile(path); readErr == nil {
				pkg := parsePythonPackageMetadata(data)
				if pkg.Name != "" {
					pkg.Path = relToApp(appPath, path)
					packages = append(packages, pkg)
				}
			}
		case strings.HasSuffix(name, ".so"):
			ext := PythonNativeExtension{Name: name, Path: relToApp(appPath, path)}
			if libs, err := s.otoolL(path); err == nil {
				ext.LinkedLibs = libs
				ext.Hints = pythonNativeExtensionHints(libs)
				ext.RiskHints = pythonNativeExtensionRiskHints(libs, name)
			}
			exts = append(exts, ext)
		}
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Name == packages[j].Name {
			return packages[i].Path < packages[j].Path
		}
		return strings.ToLower(packages[i].Name) < strings.ToLower(packages[j].Name)
	})
	sort.Slice(exts, func(i, j int) bool { return exts[i].Path < exts[j].Path })
	return packages, exts
}

func (s *pythonScan) pythonRiskHints(appPath string, app *PythonApp) []string {
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
	if s.anyLauncher(func(m pythonFileMarkers) bool { return m.envSensitive }) {
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

func (s *pythonScan) hasGlob(root, pattern string) bool {
	return s.firstMatchingFile(root, pattern) != ""
}

func (s *pythonScan) firstMatchingFile(root, pattern string) string {
	for _, e := range s.entries(root) {
		if e.d == nil || e.d.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(pattern, e.d.Name()); ok {
			return e.path
		}
	}
	return ""
}

func (s *pythonScan) hasNuitkaMarker(root string) bool {
	for _, e := range s.entries(root) {
		if e.d == nil || e.d.IsDir() {
			continue
		}
		name := strings.ToLower(e.d.Name())
		if strings.Contains(name, "nuitka") {
			return true
		}
		if strings.HasSuffix(name, ".so") && s.fileMarkers(e).nuitka {
			return true
		}
	}
	return false
}

// launcherReferencesPython reports whether any file under Contents/MacOS
// contains "python", "Python.framework" or "libpython".
func (s *pythonScan) launcherReferencesPython() bool {
	return s.anyLauncher(func(m pythonFileMarkers) bool { return m.launcherPython })
}

func (s *pythonScan) anyLauncher(hit func(pythonFileMarkers) bool) bool {
	for _, e := range s.entries(s.macos) {
		if e.d != nil && !e.d.IsDir() && hit(s.fileMarkers(e)) {
			return true
		}
	}
	return false
}

func (s *pythonScan) externalInterpreterFromLaunchers() string {
	for _, e := range s.entries(s.macos) {
		if e.d == nil || e.d.IsDir() {
			continue
		}
		if p := s.fileMarkers(e).interpreter; p != "" {
			return p
		}
	}
	return ""
}

func (s *pythonScan) versionFromLayout() string {
	for _, e := range s.entries(s.contents) {
		if e.d == nil {
			continue
		}
		if m := pythonLayoutVersionRE.FindStringSubmatch(e.d.Name()); len(m) == 2 {
			return m[1]
		}
		if e.d.Name() == "pyvenv.cfg" {
			if v := s.versionFromFile(e.path); v != "" {
				return v
			}
		}
	}
	return ""
}

func (s *pythonScan) versionFromFile(path string) string {
	if v, ok := s.versions[path]; ok {
		return v
	}
	v := s.readVersionFile(path)
	s.versions[path] = v
	return v
}

func (s *pythonScan) readVersionFile(path string) string {
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
	return pythonDottedVersionRE.FindString(string(data))
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
