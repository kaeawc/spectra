// Package detect identifies the UI framework and runtime that built a
// macOS .app bundle. Detection runs in three layers, strongest first:
//
//  1. Bundle markers (presence of named frameworks/files)
//  2. Linked dylibs reported by `otool -L` on the main executable
//  3. ASCII strings inside the main executable as a tiebreaker
//
// Layer 1 alone classifies most apps; layers 2 and 3 disambiguate
// native (SwiftUI vs AppKit) and Rust-based (Tauri vs raw Rust).
package detect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/bundleid"
)

// Result is the diagnosis for one .app bundle.
type Result struct {
	Path          string
	UI            string          // Electron, SwiftUI, AppKit, Tauri, Flutter, Qt, ComposeDesktop, Swing, SWT, NetBeansPlatform, EclipseRCP, Wxwidgets, Unknown
	Runtime       string          // Node+Chromium, Swift, ObjC, Rust, Dart, JVM, Go, C++, mixed, unknown
	Language      string          // best guess: TypeScript/JS, Swift, Kotlin, Java, Rust, Go, Dart, C++, ObjC
	Packaging     string          // Squirrel, Sparkle, jpackage, install4j, none
	Confidence    string          // high | medium | low
	Signals       []string        // human-readable reasons we picked this
	NativeModules []NativeModule  // sub-detection: custom native code embedded in the bundle
	ObjC          *ObjCInspection // Objective-C/AppKit bundle profile, when applicable

	// Metadata pulled from Info.plist and frameworks. Best-effort; any
	// field may be empty if the bundle doesn't expose it.
	BundleID          string // CFBundleIdentifier (com.example.app)
	AppVersion        string // CFBundleShortVersionString
	BuildNumber       string // CFBundleVersion
	ElectronVersion   string // version of bundled Electron Framework, if any
	FrameworkVersions map[string]string
	Architectures     []string // arm64, x86_64
	BundleSizeBytes   int64    // total disk usage of the .app (sparse-aware: Blocks*512)
	ApparentSizeBytes int64    // logical size of the .app (fi.Size() sum; may be >> BundleSizeBytes for sparse images)
	TeamID            string   // code-sign team identifier
	SparkleFeedURL    string   // SUFeedURL from Info.plist, if present
	MASReceipt        bool     // Mac App Store receipt is embedded
	HardenedRuntime   bool     // codesign reports flags=0x10000(runtime)
	Sandboxed         bool     // entitlements include com.apple.security.app-sandbox
	Entitlements      []string // notable boolean entitlements (subset)

	Storage *StorageFootprint // user-data on disk in ~/Library

	// NetworkEndpoints lists distinct hostnames referenced by URL strings
	// embedded in the binary and (for Electron) the app.asar payload.
	// Populated only when Detect was called with ScanNetwork in Options.
	NetworkEndpoints []string

	// PrivacyDescriptions are the NS*UsageDescription keys an app declares
	// in Info.plist — what it's *willing* to ask for, regardless of what
	// the user has granted.
	PrivacyDescriptions map[string]string

	// Dependencies summarises third-party frameworks and (for Electron)
	// embedded npm packages.
	Dependencies *Dependencies

	// Swift summarises Swift runtime, Apple framework, and app-group
	// signals for native Swift or Swift-hybrid apps.
	Swift *SwiftInspection

	// Helpers enumerates the bundle's sub-bundles: helper apps (Electron's
	// GPU/Renderer/Plugin), XPC services, and plugins/extensions.
	Helpers *Helpers

	// LoginItems are LaunchAgent/Daemon plists installed on the system
	// that belong to this app.
	LoginItems []LoginItem

	// RunningProcesses lists currently-running processes whose executable
	// path is inside this app bundle.
	RunningProcesses []ProcessInfo
	AppStartedAt     *time.Time `json:",omitempty"` // oldest matching process start time
	AppUptimeSeconds int64      `json:",omitempty"` // seconds since AppStartedAt at inspection time

	// GrantedPermissions are the privacy permissions actually granted to
	// this bundle by the user (TCC.db). Distinct from PrivacyDescriptions
	// which only declares what the app is willing to ask for. Reading the
	// system TCC.db requires Full Disk Access for the spectra binary; the
	// per-user TCC.db is readable as long as the user runs the tool as
	// themselves. Empty when neither database is accessible.
	GrantedPermissions []string

	// GatekeeperStatus is the result of `spctl --assess --type exec` on
	// the bundle: "accepted", "rejected", or "" when spctl is unavailable
	// or the assessment could not be completed. "accepted" means the app
	// passes Gatekeeper's notarization / Developer ID check; "rejected"
	// means it would be blocked by Gatekeeper on macOS 10.15+.
	GatekeeperStatus string
}

// Helpers groups sub-bundles found inside the .app.
type Helpers struct {
	HelperApps  []string // basenames (without .app suffix) of Helper sub-apps
	XPCServices []string // basenames (without .xpc suffix)
	Plugins     []string // PlugIns/ contents (.appex, .plugin, .ideplugin, etc.)
}

// LoginItem is one launchd plist installed for this bundle.
type LoginItem struct {
	Path      string // full path to the plist
	Label     string // launchd Label (typically the bundle ID + suffix)
	Scope     string // user | system
	Daemon    bool   // true for /Library/LaunchDaemons
	RunAtLoad bool   // plist RunAtLoad key
	KeepAlive bool   // plist KeepAlive key (simple true/false form)
}

// ProcessInfo is one running process belonging to this bundle.
type ProcessInfo struct {
	PID       int
	RSSKiB    int       // resident set size, kibibytes
	Command   string    // executable path (truncated to bundle-relative)
	StartTime time.Time // process start time (from ps lstart)
}

// Dependencies enumerates the third-party libraries an app embeds.
type Dependencies struct {
	ThirdPartyFrameworks []string // names under Contents/Frameworks/, sans Apple
	NPMPackages          []string // top-level dirs under app.asar.unpacked/node_modules
	JavaJars             int      // count of .jar files (JVM apps)
}

// SwiftInspection describes Swift-specific app signals derived from shared
// bundle inspection sources.
type SwiftInspection struct {
	RuntimeLibraries  []string // libswift*.dylib basenames
	AppleFrameworks   []string // normalized linked Apple framework basenames
	UsesSwiftUI       bool
	UsesAppIntents    bool
	UsesScreenCapture bool
	AppGroups         []string
}

// ObjCInspection summarizes AppKit/Objective-C bundle structure that is
// useful once the framework classifier lands on plain AppKit.
type ObjCInspection struct {
	LinkedFrameworks       []string
	PrincipalClass         string
	MainNibFile            string
	MainStoryboardFile     string
	DocumentTypes          []ObjCDocumentType
	URLSchemes             []string
	Services               []string
	AutomationEntitlements []string
	UpdateMechanism        string
}

// ObjCDocumentType is one CFBundleDocumentTypes entry from Info.plist.
type ObjCDocumentType struct {
	Name       string
	Role       string
	Extensions []string
}

// StorageFootprint is the on-disk size, in bytes, of each well-known
// user-data location belonging to this bundle. Locations that don't
// exist contribute 0. Path values are populated only for non-empty
// locations so callers can show them.
type StorageFootprint struct {
	ApplicationSupport int64
	Caches             int64
	Containers         int64 // sandboxed apps; under ~/Library/Containers
	GroupContainers    int64
	HTTPStorages       int64
	WebKit             int64
	Logs               int64
	Preferences        int64 // size of the .plist file
	Total              int64

	// Paths actually found, for display.
	Paths []string
}

// NativeModule describes a native add-on found inside an Electron bundle's
// app.asar.unpacked tree. The Language field is the best-guess source
// language inferred from link map and binary content.
type NativeModule struct {
	Name           string // basename, e.g. "computer_use.node"
	Path           string // bundle-relative path
	PackageName    string // npm package name when package.json is present
	PackageVersion string // npm package version when package.json is present
	Language       string // Rust, Swift, C++, unknown
	Hints          []string
	RiskHints      []string // security-sensitive capability patterns to review
}

// Options controls optional, more expensive sub-detections.
type Options struct {
	ScanNetwork bool // scan binary + app.asar for embedded URL hosts
	Now         func() time.Time
}

type nodeAppFS interface {
	Exists(path string) bool
	IsDir(path string) bool
	Open(path string) (io.ReadCloser, error)
	ReadDir(path string) ([]os.DirEntry, error)
	ReadFile(path string) ([]byte, error)
	WalkDir(root string, fn fs.WalkDirFunc) error
}

type nodeAppCommands interface {
	OtoolL(path string) []string
}

type osNodeAppFS struct{}

func (osNodeAppFS) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (osNodeAppFS) IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (osNodeAppFS) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (osNodeAppFS) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (osNodeAppFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osNodeAppFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}

type execNodeAppCommands struct{}

func (execNodeAppCommands) OtoolL(path string) []string {
	return otoolL(path)
}

type nodeAppInspector struct {
	fs  nodeAppFS
	cmd nodeAppCommands
}

func defaultNodeAppInspector() nodeAppInspector {
	return nodeAppInspector{fs: osNodeAppFS{}, cmd: execNodeAppCommands{}}
}

type markerScanner interface {
	ScanMarkers(exe string) binaryMarkers
}

type fileMarkerScanner struct{}

func (fileMarkerScanner) ScanMarkers(exe string) binaryMarkers {
	return scanBinaryMarkers(exe)
}

// Detect inspects the bundle at appPath and returns a Result.
// It never returns an error for "unknown" — instead it fills the result
// with what it found and Confidence="low".
func Detect(appPath string) (Result, error) {
	return DetectWith(appPath, Options{})
}

// DetectWith is Detect with explicit options for callers that want the
// optional, more expensive sub-detections (network endpoints).
func DetectWith(appPath string, opts Options) (Result, error) {
	r := Result{Path: appPath, UI: "Unknown", Runtime: "unknown", Confidence: "low"}

	info, err := os.Stat(appPath)
	if err != nil {
		return r, fmt.Errorf("stat %s: %w", appPath, err)
	}
	if !info.IsDir() || !strings.HasSuffix(appPath, ".app") {
		return r, fmt.Errorf("%s is not a .app bundle", appPath)
	}

	exe, err := mainExecutable(appPath)
	if err != nil {
		r.Signals = append(r.Signals, "could not resolve main executable: "+err.Error())
	}
	exe = followWrapper(exe)

	// Layer 1: bundle markers. Decisive for most apps.
	l1Decided := classifyByBundleMarkers(appPath, &r)

	if !l1Decided {
		// Browser-style framework shims (Chrome, Edge, Brave) hide their
		// real implementation in a sibling .framework. Follow it before L2/L3.
		exe = followFrameworkShim(appPath, exe, &r)
		// Layer 2: linked dylibs (sets a tentative UI for AppKit/SwiftUI/etc.).
		classifyByLinkedLibs(exe, &r)
		// Layer 3: Rust/Go markers can override an AppKit guess
		// (Warp, Zed, Conductor, Ollama all link AppKit but are Rust/Go).
		classifyByStrings(exe, &r)
	}

	enrichFromExe(exe, &r)

	// Sub-detection: native add-ons inside app.asar.unpacked. Only meaningful
	// for Electron, where they reveal hybrid architectures (Claude's Rust +
	// Swift bridges vs Codex's plain JS).
	if r.UI == "Electron" {
		r.NativeModules = scanNativeModules(appPath)
	}

	populateMetadata(appPath, exe, &r)
	r.PrivacyDescriptions = readPrivacyDescriptions(appPath)
	r.Dependencies = scanDependencies(appPath)
	r.Helpers = scanHelpers(appPath)
	r.ObjC = scanObjCInspection(appPath, exe, r)
	r.LoginItems = scanLoginItems(appPath, r.BundleID)
	r.RunningProcesses = scanRunningProcesses(appPath)
	r.AppStartedAt, r.AppUptimeSeconds = appUptime(r.RunningProcesses, detectNow(opts))
	r.GrantedPermissions = scanGrantedPermissions(r.BundleID)
	r.GatekeeperStatus = readGatekeeperStatus(appPath)
	if opts.ScanNetwork {
		r.NetworkEndpoints = scanNetworkEndpoints(appPath, exe)
	}
	return r, nil
}

func detectNow(opts Options) time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

// populateMetadata fills the metadata fields on Result. Each piece is
// best-effort and silently skipped on failure — these are decoration,
// not part of the verdict.
func populateMetadata(appPath, exe string, r *Result) {
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	r.BundleID = readPlistString(plist, "CFBundleIdentifier")
	r.AppVersion = readPlistString(plist, "CFBundleShortVersionString")
	r.BuildNumber = readPlistString(plist, "CFBundleVersion")
	r.SparkleFeedURL = readPlistString(plist, "SUFeedURL")

	if r.UI == "Electron" {
		efw := filepath.Join(appPath, "Contents", "Frameworks", "Electron Framework.framework", "Resources", "Info.plist")
		r.ElectronVersion = readPlistString(efw, "CFBundleVersion")
	}
	r.FrameworkVersions = frameworkVersions(appPath, r)

	if exe != "" {
		r.Architectures = readArchitectures(exe)
	}
	r.BundleSizeBytes, r.ApparentSizeBytes = bundleSizes(appPath)
	r.TeamID, r.HardenedRuntime = readSigning(appPath)
	var appGroups []string
	r.Sandboxed, r.Entitlements, appGroups = readEntitlementsDetail(appPath)
	r.MASReceipt = exists(filepath.Join(appPath, "Contents", "_MASReceipt"))
	r.Storage = scanStorage(appPath, r.BundleID)
	r.Swift = inspectSwiftApp(appPath, exe, realSwiftInspectionSource{appGroups: appGroups})
}

func frameworkVersions(appPath string, r *Result) map[string]string {
	versions := make(map[string]string)
	if r.ElectronVersion != "" {
		versions["Electron"] = r.ElectronVersion
	}
	flutterPlist := filepath.Join(appPath, "Contents", "Frameworks", "FlutterMacOS.framework", "Resources", "Info.plist")
	if v := firstPlistString(flutterPlist, "CFBundleShortVersionString", "CFBundleVersion"); v != "" {
		versions["Flutter"] = v
	}
	qtPlist := filepath.Join(appPath, "Contents", "Frameworks", "QtCore.framework", "Resources", "Info.plist")
	if v := firstPlistString(qtPlist, "CFBundleShortVersionString", "CFBundleVersion"); v != "" {
		versions["Qt"] = v
	}
	if v := readTauriVersion(appPath); v != "" {
		versions["Tauri"] = v
	}
	if len(versions) == 0 {
		return nil
	}
	return versions
}

func firstPlistString(plist string, keys ...string) string {
	for _, key := range keys {
		if v := readPlistString(plist, key); v != "" {
			return v
		}
	}
	return ""
}

func readTauriVersion(appPath string) string {
	return readTauriVersionWith(appPath, osNodeAppFS{})
}

func readTauriVersionWith(appPath string, files nodeAppFS) string {
	for _, rel := range []string{
		filepath.Join("Contents", "Resources", "tauri.conf.json"),
		filepath.Join("Contents", "Resources", "tauri.conf.json5"),
	} {
		data, err := files.ReadFile(filepath.Join(appPath, rel))
		if err != nil {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if pkg, ok := m["package"].(map[string]any); ok {
			if v, ok := pkg["version"].(string); ok && v != "" {
				return v
			}
		}
		if v, ok := m["version"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// scanStorage measures user-data sizes under ~/Library for this bundle.
// macOS apps spread state across half a dozen locations; we sum them so
// callers can see the real on-disk cost. Apps register state under both
// the bundle ID and a human display name (e.g. "Claude" + "com.anthropic
// .claudefordesktop"), so we probe both.
func scanStorage(appPath, bundleID string) *StorageFootprint {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	displayName := strings.TrimSuffix(filepath.Base(appPath), ".app")
	keys := []string{}
	if bundleID != "" {
		keys = append(keys, bundleID)
	}
	if displayName != "" && displayName != bundleID {
		keys = append(keys, displayName)
	}

	s := &StorageFootprint{}
	probe := func(parent string, target *int64) {
		for _, k := range keys {
			p := filepath.Join(home, "Library", parent, k)
			if !exists(p) {
				continue
			}
			n := bundleSize(p) // recursive sum (ok for non-.app dirs too)
			if n == 0 {
				continue
			}
			*target += n
			s.Paths = append(s.Paths, p)
		}
	}

	probe("Application Support", &s.ApplicationSupport)
	probe("Caches", &s.Caches)
	probe("Containers", &s.Containers)
	probe("Group Containers", &s.GroupContainers)
	probe("HTTPStorages", &s.HTTPStorages)
	probe("WebKit", &s.WebKit)
	probe("Logs", &s.Logs)

	// Preferences: a single .plist file per bundle.
	for _, k := range keys {
		p := filepath.Join(home, "Library", "Preferences", k+".plist")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			s.Preferences += fi.Size()
			s.Paths = append(s.Paths, p)
		}
	}

	s.Total = s.ApplicationSupport + s.Caches + s.Containers +
		s.GroupContainers + s.HTTPStorages + s.WebKit +
		s.Logs + s.Preferences

	if s.Total == 0 {
		return nil
	}
	return s
}

func readPlistString(plist, key string) string {
	out, err := exec.Command("plutil", "-extract", key, "raw", "-o", "-", plist).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readArchitectures inspects the Mach-O header(s) of exe via `file` and
// returns the architectures present. Universal binaries report both.
func readArchitectures(exe string) []string {
	out, err := exec.Command("file", exe).Output()
	if err != nil {
		return nil
	}
	var arches []string
	if bytes.Contains(out, []byte("arm64")) {
		arches = append(arches, "arm64")
	}
	if bytes.Contains(out, []byte("x86_64")) {
		arches = append(arches, "x86_64")
	}
	return arches
}

// bundleSizes walks appPath once and returns (actual, apparent) byte counts
// for all regular files. actual uses Stat_t.Blocks*512 so sparse files report
// real allocation; apparent uses fi.Size() (the logical ceiling). For
// non-sparse files they are equal; a Docker VM disk image may show
// apparent 60 GiB vs actual 4 GiB. Errors are ignored; partial sums are fine.
func bundleSizes(appPath string) (actual, apparent int64) {
	_ = filepath.WalkDir(appPath, func(_ string, d os.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		fi, _ := d.Info()
		if fi == nil || !fi.Mode().IsRegular() {
			return nil
		}
		actual += diskBytes(fi)
		apparent += fi.Size()
		return nil
	})
	return actual, apparent
}

// bundleSize returns the actual (sparse-aware) disk usage for appPath.
// Deprecated: prefer bundleSizes when you also need apparent size.
func bundleSize(appPath string) int64 {
	a, _ := bundleSizes(appPath)
	return a
}

// readSigning parses `codesign -dv` stderr for the team identifier and
// the hardened-runtime flag (flags=0x10000(runtime)).
func readSigning(appPath string) (teamID string, hardened bool) {
	cmd := exec.Command("codesign", "-dv", appPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // codesign writes its info to stderr regardless
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.HasPrefix(line, "TeamIdentifier=") {
			v := strings.TrimPrefix(line, "TeamIdentifier=")
			if v != "not set" {
				teamID = v
			}
		}
		if strings.Contains(line, "flags=") && strings.Contains(line, "(runtime)") {
			hardened = true
		}
	}
	return teamID, hardened
}

// readEntitlements asks codesign for the bundle's entitlements plist
// (XML form) and extracts the boolean ones set to true. The full set
// is large and noisy; we keep a curated allowlist of notable ones.
// readGatekeeperStatus runs `spctl --assess --type exec <appPath>` and
// returns "accepted", "rejected", or "" when spctl is unavailable or the
// assessment cannot be completed. spctl writes its verdict to stderr
// regardless of exit code, so we parse stderr for the verdict string.
func readGatekeeperStatus(appPath string) string {
	cmd := exec.Command("spctl", "--assess", "--type", "exec", appPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return parseGatekeeperOutput(stderr.String())
}

// parseGatekeeperOutput extracts the Gatekeeper verdict from spctl stderr.
// Expected formats:
//
//	"/Applications/Foo.app: accepted"
//	"/Applications/Foo.app: rejected"
func parseGatekeeperOutput(out string) string {
	if strings.Contains(out, "accepted") {
		return "accepted"
	}
	if strings.Contains(out, "rejected") {
		return "rejected"
	}
	return ""
}

func readEntitlements(appPath string) (sandboxed bool, notable []string) {
	sandboxed, notable, _ = readEntitlementsDetail(appPath)
	return sandboxed, notable
}

func readEntitlementsDetail(appPath string) (sandboxed bool, notable, appGroups []string) {
	out, err := exec.Command("codesign", "-d", "--entitlements", ":-", appPath).Output()
	if err != nil || len(out) == 0 {
		return false, nil, nil
	}

	// Pairs look like <key>NAME</key><true/> in the single-line XML output.
	xml := string(out)
	appGroups = applicationGroups(xml)
	notableKeys := map[string]bool{
		"com.apple.security.app-sandbox":                         true,
		"com.apple.security.network.client":                      true,
		"com.apple.security.network.server":                      true,
		"com.apple.security.device.camera":                       true,
		"com.apple.security.device.audio-input":                  true,
		"com.apple.security.device.bluetooth":                    true,
		"com.apple.security.device.usb":                          true,
		"com.apple.security.personal-information.location":       true,
		"com.apple.security.cs.allow-jit":                        true,
		"com.apple.security.cs.disable-library-validation":       true,
		"com.apple.security.cs.allow-unsigned-executable-memory": true,
		"com.apple.security.automation.apple-events":             true,
		"com.apple.security.virtualization":                      true,
	}

	for key := range notableKeys {
		needle := "<key>" + key + "</key><true/>"
		if strings.Contains(xml, needle) {
			if key == "com.apple.security.app-sandbox" {
				sandboxed = true
			}
			notable = append(notable, strings.TrimPrefix(key, "com.apple.security."))
		}
	}
	sort.Strings(notable)
	return sandboxed, notable, appGroups
}

// --- Layer 1 ----------------------------------------------------------------

func classifyByBundleMarkers(appPath string, r *Result) bool {
	frameworks := filepath.Join(appPath, "Contents", "Frameworks")
	resources := filepath.Join(appPath, "Contents", "Resources")
	contents := filepath.Join(appPath, "Contents")

	// Electron: Electron Framework + (app.asar or app/ dir)
	if exists(filepath.Join(frameworks, "Electron Framework.framework")) {
		hasAsar := exists(filepath.Join(resources, "app.asar"))
		hasAppDir := isDir(filepath.Join(resources, "app"))
		if hasAsar || hasAppDir {
			setHighConfidence(r, "Electron", "Node+Chromium", "TypeScript/JS", "Frameworks/Electron Framework.framework")
			if hasAsar {
				r.Signals = append(r.Signals, "Resources/app.asar")
			} else {
				r.Signals = append(r.Signals, "Resources/app/")
			}
			return true
		}
	}

	// Flutter
	if exists(filepath.Join(frameworks, "FlutterMacOS.framework")) {
		setHighConfidence(r, "Flutter", "Dart", "Dart", "Frameworks/FlutterMacOS.framework")
		return true
	}

	// Qt
	if exists(filepath.Join(frameworks, "QtCore.framework")) || exists(filepath.Join(resources, "qt.conf")) {
		setHighConfidence(r, "Qt", "C++", "C++", "QtCore.framework or qt.conf")
		return true
	}

	// React Native macOS
	if exists(filepath.Join(frameworks, "React.framework")) || exists(filepath.Join(frameworks, "hermes.framework")) {
		setHighConfidence(r, "ReactNative", "Hermes/JSC", "TypeScript/JS", "React.framework or hermes.framework")
		return true
	}

	// JVM-based apps: detect bundled JRE OR jar-heavy bundle.
	jvmRoot := findJVMRoot(contents)
	jarCount := countJars(contents)
	if jvmRoot != "" || jarCount >= 5 {
		return classifyJVMMarkers(appPath, contents, jvmRoot, jarCount, r)
	}

	return false
}

func setHighConfidence(r *Result, ui, runtime, language, signal string) {
	r.UI = ui
	r.Runtime = runtime
	r.Language = language
	r.Confidence = "high"
	r.Signals = append(r.Signals, signal)
}

func classifyJVMMarkers(appPath, contents, jvmRoot string, jarCount int, r *Result) bool {
	if jvmRoot != "" {
		r.Signals = append(r.Signals, "bundled JVM at "+rel(appPath, jvmRoot))
	} else {
		r.Signals = append(r.Signals, fmt.Sprintf("%d .jar files in bundle (no embedded JVM)", jarCount))
	}
	r.Runtime = "JVM"
	r.Language = "Java"
	r.Confidence = "high"

	switch {
	case hasFileLike(contents, "libskiko-macos"):
		r.UI = "ComposeDesktop"
		r.Language = "Kotlin"
		r.Signals = append(r.Signals, "libskiko-macos-*.dylib")
	case hasFileLike(contents, "org.eclipse.osgi"):
		r.UI = "EclipseRCP"
		r.Signals = append(r.Signals, "org.eclipse.osgi plugin")
	case hasFileLike(contents, "i4jruntime.jar") || hasFileLike(contents, ".install4j"):
		r.UI = "Swing"
		r.Packaging = "install4j"
		r.Signals = append(r.Signals, "install4j launcher")
	case hasFileLike(contents, "org-netbeans"):
		r.UI = "NetBeansPlatform"
		r.Signals = append(r.Signals, "org-netbeans-* jar")
	default:
		r.UI = "Swing/JavaFX (JVM)"
		r.Confidence = "medium"
	}
	return true
}

// --- Layer 2: linked dylibs --------------------------------------------------

func classifyByLinkedLibs(exe string, r *Result) {
	classifyByLinkedLibsWith(exe, r, execNodeAppCommands{})
}

func classifyByLinkedLibsWith(exe string, r *Result, cmds nodeAppCommands) {
	if exe == "" {
		return
	}
	libs := cmds.OtoolL(exe)
	if len(libs) == 0 {
		return
	}
	joined := strings.Join(libs, "\n")

	hasSwiftUI := strings.Contains(joined, "/SwiftUI.framework/")
	hasSwiftRT := strings.Contains(joined, "/libswift")
	hasAppKit := strings.Contains(joined, "/AppKit.framework/") || strings.Contains(joined, "/Cocoa.framework/")
	hasWebKit := strings.Contains(joined, "/WebKit.framework/")
	// Mac Catalyst apps link UIKit from /System/iOSSupport/ (the iOS-on-Mac
	// runtime). UIKitMacHelper is the bridging shim. The presence of either
	// is a definitive Catalyst signal — these paths never appear in plain
	// AppKit or SwiftUI apps.
	hasCatalyst := strings.Contains(joined, "/iOSSupport/") ||
		strings.Contains(joined, "/UIKitMacHelper.framework/")

	if hasCatalyst {
		switch {
		case hasSwiftUI:
			r.UI = "MacCatalyst+SwiftUI"
		default:
			r.UI = "MacCatalyst"
		}
		r.Runtime = "Swift"
		if hasSwiftRT {
			r.Language = "Swift"
		} else {
			r.Language = "Obj-C/Swift"
		}
		r.Confidence = "high"
		r.Signals = append(r.Signals, "links UIKit from /System/iOSSupport (Catalyst)")
		return
	}

	switch {
	case hasSwiftUI:
		r.UI = "SwiftUI"
		r.Runtime = "Swift"
		r.Language = "Swift"
		r.Confidence = "high"
		r.Signals = append(r.Signals, "links SwiftUI.framework")
	case hasSwiftRT:
		r.UI = "AppKit+Swift"
		r.Runtime = "Swift"
		r.Language = "Swift"
		r.Confidence = "high"
		r.Signals = append(r.Signals, "links libswift*.dylib (no SwiftUI)")
	case hasAppKit && hasWebKit:
		r.UI = "AppKit+WebKit"
		r.Runtime = "ObjC"
		r.Language = "Obj-C/Swift"
		r.Confidence = "medium"
		r.Signals = append(r.Signals, "links AppKit + WebKit (Tauri suspect)")
	case hasAppKit:
		r.UI = "AppKit"
		r.Runtime = "ObjC"
		r.Language = "Obj-C/Swift"
		r.Confidence = "medium"
		r.Signals = append(r.Signals, "links AppKit/Cocoa, no Swift dylibs")
	}
}

// --- Layer 3: binary strings -------------------------------------------------

func classifyByStrings(exe string, r *Result) {
	classifyByStringsWith(exe, r, fileMarkerScanner{})
}

func classifyByStringsWith(exe string, r *Result, scanner markerScanner) {
	if exe == "" {
		return
	}
	m := scanner.ScanMarkers(exe)

	// Go wins outright — the buildinfo magic only appears in Go binaries.
	if m.hasGoBuildID {
		switch {
		case m.isWails:
			// Confirmed Wails: Go binary with the Wails import path baked in.
			r.UI = "Wails"
			r.Signals = append(r.Signals, "wailsapp/wails import in binary")
		case r.UI == "AppKit+WebKit":
			// Go binary that draws its UI through a system WebView, but
			// without the Wails framework string. Could be a custom bridge
			// (e.g. Ollama). Use a neutral label.
			r.UI = "Go+WebKit"
		default:
			r.UI = "Native (Go)"
		}
		r.Runtime = "Go"
		r.Language = "Go"
		r.Confidence = "high"
		r.Signals = append(r.Signals, "__go_buildinfo section present")
		return
	}

	// Rust threshold: 100+ combined panic-site markers is comfortably above
	// the noise floor (a single bundled Rust dylib in an otherwise non-Rust
	// app produces <30).
	if m.rustHits >= 100 {
		switch r.UI {
		case "AppKit+WebKit":
			r.UI = "Tauri"
		default:
			r.UI = "Native (Rust)"
		}
		r.Runtime = "Rust"
		r.Language = "Rust"
		r.Confidence = "high"
		r.Signals = append(r.Signals, fmt.Sprintf("%d Rust panic-site strings", m.rustHits))
		return
	}

	// WKWebView wrapper: native shell + bundled web assets + nothing else
	// distinctive. These are AppKit/Swift apps whose entire UI is a single
	// WKWebView pointed at a local index.html.
	if r.UI == "AppKit+WebKit" {
		appPath := bundleRootFromExe(exe)
		if appPath != "" && hasBundledWebApp(appPath) {
			r.UI = "WKWebView wrapper"
			r.Runtime = "ObjC"
			r.Confidence = "medium"
			r.Signals = append(r.Signals, "bundled web assets (index.html) under Resources/")
		}
	}
}

// bundleRootFromExe walks back from Contents/MacOS/<exe> to the .app root.
func bundleRootFromExe(exe string) string {
	if exe == "" {
		return ""
	}
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	if filepath.Base(contents) != "Contents" {
		return ""
	}
	return filepath.Dir(contents)
}

// hasBundledWebApp returns true if the bundle ships an index.html anywhere
// under Contents/Resources — the smoking gun of a WKWebView wrapper.
func hasBundledWebApp(appPath string) bool {
	res := filepath.Join(appPath, "Contents", "Resources")
	var hit bool
	_ = filepath.WalkDir(res, func(_ string, d os.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if d.Name() == "index.html" {
			hit = true
			return io.EOF
		}
		return nil
	})
	return hit
}

// --- Enrichment: packaging hints --------------------------------------------

func enrichFromExe(exe string, r *Result) {
	frameworks := filepath.Join(filepath.Dir(filepath.Dir(exe)), "Frameworks")
	if r.Packaging == "" {
		switch {
		case exists(filepath.Join(frameworks, "Sparkle.framework")):
			r.Packaging = "Sparkle"
		case exists(filepath.Join(frameworks, "Squirrel.framework")):
			r.Packaging = "Squirrel"
		}
	}
}

// --- Helpers ----------------------------------------------------------------

// mainExecutable resolves the real executable inside the bundle by reading
// CFBundleExecutable from Info.plist via plutil.
func mainExecutable(appPath string) (string, error) {
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	out, err := exec.Command("plutil", "-extract", "CFBundleExecutable", "raw", "-o", "-", plist).Output()
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return "", fmt.Errorf("CFBundleExecutable empty in %s", plist)
	}
	exe := filepath.Join(appPath, "Contents", "MacOS", name)
	if !exists(exe) {
		return "", fmt.Errorf("executable %s not found", exe)
	}
	return exe, nil
}

func otoolL(exe string) []string {
	out, err := exec.Command("otool", "-L", exe).Output()
	if err != nil {
		return nil
	}
	return parseOtoolLibraries(out)
}

// scanBinaryMarkers streams the binary looking for Rust and Go fingerprints,
// plus framework-specific tells (Wails). We avoid `strings(1)` to skip a fork
// and to handle multi-arch binaries uniformly.
func scanBinaryMarkers(exe string) (m binaryMarkers) {
	return defaultNodeAppInspector().scanBinaryMarkers(exe)
}

func (ins nodeAppInspector) scanBinaryMarkers(exe string) (m binaryMarkers) {
	f, err := ins.fs.Open(exe)
	if err != nil {
		return m
	}
	defer f.Close()

	rustNeedles := [][]byte{
		[]byte("core::panicking"),
		[]byte("rust_panic"),
		[]byte("rustc/"),
		[]byte("panicked at "),
		[]byte("RUST_BACKTRACE"),
	}
	goNeedle := []byte("\xff Go buildinf:") // prefix of __go_buildinfo magic
	wailsNeedle := []byte("github.com/wailsapp/wails")

	buf := make([]byte, 1<<20) // 1MB chunks
	overlap := 64
	tail := make([]byte, 0, overlap)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			region := append(tail, buf[:n]...)
			for _, needle := range rustNeedles {
				m.rustHits += bytes.Count(region, needle)
			}
			if !m.hasGoBuildID && bytes.Contains(region, goNeedle) {
				m.hasGoBuildID = true
			}
			if !m.isWails && bytes.Contains(region, wailsNeedle) {
				m.isWails = true
			}
			if len(region) > overlap {
				tail = append(tail[:0], region[len(region)-overlap:]...)
			} else {
				tail = append(tail[:0], region...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	return m
}

type binaryMarkers struct {
	rustHits     int
	hasGoBuildID bool
	isWails      bool
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// findJVMRoot returns the path of the bundled JVM directory if any
// (Contents/runtime, Contents/jbr, Contents/jre, or anywhere with libjvm.dylib).
func findJVMRoot(contents string) string {
	for _, name := range []string{"runtime", "jbr", "jre", "PlugIns/jdk"} {
		p := filepath.Join(contents, name)
		if isDir(p) {
			return p
		}
	}
	// Fallback walk (cheap; bundles are shallow).
	var found string
	_ = filepath.WalkDir(contents, func(path string, d os.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if d.Name() == "libjvm.dylib" {
			found = filepath.Dir(path)
			return io.EOF
		}
		return nil
	})
	return found
}

// scanNativeModules walks Electron's unpacked payload roots for *.node
// add-ons and classifies each by language using its link map and binary
// fingerprint.
func scanNativeModules(appPath string) []NativeModule {
	return defaultNodeAppInspector().scanNativeModules(appPath)
}

func (ins nodeAppInspector) scanNativeModules(appPath string) []NativeModule {
	var mods []NativeModule
	seen := map[string]struct{}{}
	for _, root := range ins.nativeModuleRoots(appPath) {
		_ = ins.fs.WalkDir(root, func(path string, d os.DirEntry, _ error) error {
			if d == nil || d.IsDir() {
				return nil
			}
			// Skip dSYM debug bundles and source maps.
			if strings.Contains(path, ".dSYM/") {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".node") {
				return nil
			}
			rel, _ := filepath.Rel(appPath, path)
			if _, ok := seen[rel]; ok {
				return nil
			}
			seen[rel] = struct{}{}
			mods = append(mods, ins.classifyNativeModule(path, rel))
			return nil
		})
	}
	sort.Slice(mods, func(i, j int) bool {
		return mods[i].Path < mods[j].Path
	})
	return mods
}

func nativeModuleRoots(appPath string) []string {
	return defaultNodeAppInspector().nativeModuleRoots(appPath)
}

func (ins nodeAppInspector) nativeModuleRoots(appPath string) []string {
	resources := filepath.Join(appPath, "Contents", "Resources")
	candidates := []string{
		filepath.Join(resources, "app.asar.unpacked"),
		filepath.Join(resources, "app"),
	}
	var roots []string
	for _, root := range candidates {
		if ins.fs.IsDir(root) {
			roots = append(roots, root)
		}
	}
	return roots
}

// classifyNativeModule infers a language from a single .node file. The
// link map is the strongest signal: Cargo target paths reveal Rust, and
// libswift* references reveal Swift. Everything else falls through to
// rust-strings scanning, then C++.
func classifyNativeModule(absPath, relPath string) NativeModule {
	return defaultNodeAppInspector().classifyNativeModule(absPath, relPath)
}

func (ins nodeAppInspector) classifyNativeModule(absPath, relPath string) NativeModule {
	m := NativeModule{Name: filepath.Base(absPath), Path: relPath, Language: "C++"}
	ins.readNativeModulePackage(absPath, &m)
	appendNativeModuleCapabilityHints(&m)
	appendNativeModuleRiskHints(&m)
	libs := ins.cmd.OtoolL(absPath)
	joined := strings.Join(libs, "\n")

	// Resolve @rpath references to actual sibling dylibs so we can also
	// inspect the real implementation library (e.g. libClaudeSwift.dylib).
	sidecars := ins.nativeModuleSidecars(absPath, libs, &m)

	// Rust signal #1: Cargo target path leaks into the load command.
	if strings.Contains(joined, "/target/") && (strings.Contains(joined, "-apple-darwin/release/") || strings.Contains(joined, "-apple-darwin/debug/")) {
		m.Language = "Rust"
		m.Hints = append(m.Hints, "cargo target path in link map")
		return m
	}

	// Swift signal: links libswift* OR a sidecar named libSwift*/libClaudeSwift/etc.
	hasSwiftLib := strings.Contains(joined, "/libswift") || strings.Contains(joined, "/Swift.framework/")
	for _, s := range sidecars {
		if strings.Contains(strings.ToLower(filepath.Base(s)), "swift") {
			hasSwiftLib = true
		}
	}
	if hasSwiftLib {
		m.Language = "Swift"
		m.Hints = append(m.Hints, "links Swift runtime / Swift sidecar dylib")
		// Notable framework hints worth surfacing.
		for _, fw := range []string{"ScreenCaptureKit", "AVFAudio", "Combine", "Metal", "AppKit"} {
			if strings.Contains(joined, "/"+fw+".framework/") {
				m.Hints = append(m.Hints, "uses "+fw)
			}
		}
		return m
	}

	// Rust signal #2: panic strings in the .node file or any sidecar.
	rust := ins.scanBinaryMarkers(absPath).rustHits
	for _, s := range sidecars {
		rust += ins.scanBinaryMarkers(s).rustHits
	}
	if rust >= 50 {
		m.Language = "Rust"
		m.Hints = append(m.Hints, fmt.Sprintf("%d Rust panic-site strings", rust))
		return m
	}

	// Default: plain C/C++ N-API binding.
	if strings.Contains(joined, "libc++") {
		m.Hints = append(m.Hints, "links libc++")
	}
	return m
}

var nativeModuleCapabilityHints = map[string][]string{
	"@serialport/bindings-cpp": {"uses serial devices"},
	"better-sqlite3":           {"uses local SQLite native binding"},
	"fsevents":                 {"watches filesystem events"},
	"iohook":                   {"can observe global input events"},
	"keytar":                   {"uses macOS Keychain"},
	"node-hid":                 {"uses HID devices"},
	"node-keytar":              {"uses macOS Keychain"},
	"node-mac-notifier":        {"uses Notification Center"},
	"node-pty":                 {"uses pseudoterminals"},
	"robotjs":                  {"can synthesize keyboard/mouse input"},
	"sqlite3":                  {"uses local SQLite native binding"},
	"uiohook-napi":             {"can observe global input events"},
	"usb":                      {"uses USB devices"},
}

func appendNativeModuleCapabilityHints(m *NativeModule) {
	if m.PackageName == "" {
		return
	}
	m.Hints = append(m.Hints, nativeModuleCapabilityHints[strings.ToLower(m.PackageName)]...)
}

var nativeModuleRiskHints = map[string][]string{
	"@serialport/bindings-cpp": {"external device access"},
	"fsevents":                 {"filesystem activity monitoring"},
	"iohook":                   {"global input monitoring"},
	"keytar":                   {"credential store access"},
	"node-hid":                 {"external device access"},
	"node-keytar":              {"credential store access"},
	"node-mac-permissions":     {"privacy permission probing"},
	"node-pty":                 {"shell or terminal process control"},
	"robotjs":                  {"synthetic input control"},
	"uiohook-napi":             {"global input monitoring"},
	"usb":                      {"external device access"},
}

func appendNativeModuleRiskHints(m *NativeModule) {
	if m.PackageName == "" {
		return
	}
	m.RiskHints = append(m.RiskHints, nativeModuleRiskHints[strings.ToLower(m.PackageName)]...)
}

func readNativeModulePackage(absPath string, m *NativeModule) {
	defaultNodeAppInspector().readNativeModulePackage(absPath, m)
}

func (ins nodeAppInspector) readNativeModulePackage(absPath string, m *NativeModule) {
	root := nativeModulePackageRoot(absPath)
	if root == "" {
		return
	}
	data, err := ins.fs.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}
	m.PackageName = pkg.Name
	m.PackageVersion = pkg.Version
}

func nativeModulePackageRoot(absPath string) string {
	parts := strings.Split(filepath.Clean(absPath), string(os.PathSeparator))
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "node_modules" || i+1 >= len(parts) {
			continue
		}
		end := i + 2
		if strings.HasPrefix(parts[i+1], "@") {
			if i+2 >= len(parts) {
				return ""
			}
			end = i + 3
		}
		root := filepath.Join(parts[:end]...)
		if filepath.IsAbs(absPath) && !filepath.IsAbs(root) {
			root = string(os.PathSeparator) + root
		}
		return root
	}
	return ""
}

func nativeModuleSidecars(absPath string, libs []string, m *NativeModule) []string {
	return defaultNodeAppInspector().nativeModuleSidecars(absPath, libs, m)
}

func (ins nodeAppInspector) nativeModuleSidecars(absPath string, libs []string, m *NativeModule) []string {
	var sidecars []string
	for _, lib := range libs {
		if !strings.HasPrefix(lib, "@rpath/") {
			continue
		}
		sib := filepath.Join(filepath.Dir(absPath), strings.TrimPrefix(lib, "@rpath/"))
		if ins.fs.Exists(sib) {
			sidecars = append(sidecars, sib)
			m.Hints = append(m.Hints, "rpath sibling: "+filepath.Base(sib))
		}
	}
	return sidecars
}

// followFrameworkShim handles browser-style apps whose CFBundleExecutable
// is a small launcher that loads a sibling "<AppName> Framework.framework"
// (Chrome, Edge, Brave, Vivaldi all use this pattern). When the main exe
// is suspiciously small AND a same-named framework exists, the framework's
// own binary is the real implementation worth analysing.
func followFrameworkShim(appPath, exe string, r *Result) string {
	if exe == "" {
		return exe
	}
	info, err := os.Stat(exe)
	if err != nil || info.Size() > 512*1024 {
		return exe
	}
	frameworks := filepath.Join(appPath, "Contents", "Frameworks")
	entries, err := os.ReadDir(frameworks)
	if err != nil {
		return exe
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, " Framework.framework") {
			continue
		}
		fwBase := strings.TrimSuffix(name, ".framework")
		// Resolve Versions/Current/<binary> first; fall back to the top-level.
		candidates := []string{
			filepath.Join(frameworks, name, "Versions", "Current", fwBase),
			filepath.Join(frameworks, name, fwBase),
		}
		for _, c := range candidates {
			if exists(c) {
				r.Signals = append(r.Signals, "shim launcher → "+name)
				return c
			}
		}
	}
	return exe
}

// followWrapper handles bundles whose CFBundleExecutable is a tiny shim
// (e.g. Audacity's "Wrapper") that loads a sibling binary in the same
// MacOS/ directory. If the resolved exe is suspiciously small and a
// larger sibling exists, return that sibling instead.
func followWrapper(exe string) string {
	if exe == "" {
		return exe
	}
	info, err := os.Stat(exe)
	if err != nil || info.Size() > 256*1024 {
		return exe
	}
	dir := filepath.Dir(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return exe
	}
	var best string
	var bestSize int64
	for _, e := range entries {
		if e.IsDir() || filepath.Join(dir, e.Name()) == exe {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.Mode()&0o111 == 0 {
			continue // not executable
		}
		if fi.Size() > bestSize {
			best = filepath.Join(dir, e.Name())
			bestSize = fi.Size()
		}
	}
	if bestSize > 1024*1024 { // >1MB sibling: trust it as the real binary
		return best
	}
	return exe
}

// countJars returns the number of .jar files anywhere under root.
// Bounded by walk; bundles aren't deep.
func countJars(root string) int {
	return defaultNodeAppInspector().countJars(root)
}

func (ins nodeAppInspector) countJars(root string) int {
	var n int
	_ = ins.fs.WalkDir(root, func(_ string, d os.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".jar") {
			n++
		}
		return nil
	})
	return n
}

// hasFileLike returns true if any file under root contains the substring
// in its basename. Bounded walk; bundles aren't deep.
func hasFileLike(root, sub string) bool {
	var hit bool
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, _ error) error {
		if d == nil {
			return nil
		}
		if !d.IsDir() && strings.Contains(d.Name(), sub) {
			hit = true
			return io.EOF
		}
		return nil
	})
	return hit
}

// scanGrantedPermissions queries the TCC databases for which privacy
// services have been granted to this bundle ID. We check both the
// per-user database (under ~/Library) and the system one (under
// /Library); the latter requires Full Disk Access for the spectra
// binary. When auth_value is 2 (allowed) or 3 (limited / always-allow)
// the service is included. Result is a deduped, sorted slice of
// human-readable service names with the kTCCService prefix stripped.
func scanGrantedPermissions(bundleID string) []string {
	if bundleID == "" {
		return nil
	}
	dbs := []string{
		filepath.Join(home(), "Library", "Application Support", "com.apple.TCC", "TCC.db"),
		"/Library/Application Support/com.apple.TCC/TCC.db",
	}
	seen := map[string]struct{}{}
	for _, db := range dbs {
		if !exists(db) {
			continue
		}
		// The query is parameterised via SQL string concatenation rather
		// than CLI arg substitution because sqlite3(1) on macOS does not
		// accept bind variables on the command line. We sanitize the
		// bundle ID — only the limited charset reverse-DNS bundle IDs use
		// is allowed through.
		if !validBundleID(bundleID) {
			return nil
		}
		query := fmt.Sprintf(
			"SELECT service FROM access WHERE client = '%s' AND auth_value >= 2;",
			bundleID,
		)
		out, err := exec.Command("sqlite3", db, query).Output()
		if err != nil {
			continue // typically: SIP-protected DB, no FDA — skip silently
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			seen[strings.TrimPrefix(line, "kTCCService")] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validBundleID returns true if s only contains characters allowed in
// reverse-DNS bundle identifiers. Used to gate SQL-string interpolation
// in scanGrantedPermissions where parameter binding isn't available.
func validBundleID(s string) bool {
	return bundleid.Valid(s)
}

// scanHelpers enumerates sub-bundles inside the .app: helper apps,
// XPC services, and plugins/extensions. Empty result returns nil.
func scanHelpers(appPath string) *Helpers {
	h := &Helpers{}
	frameworks := filepath.Join(appPath, "Contents", "Frameworks")
	if entries, err := os.ReadDir(frameworks); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".app") {
				h.HelperApps = append(h.HelperApps, strings.TrimSuffix(e.Name(), ".app"))
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(appPath, "Contents", "XPCServices")); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".xpc") {
				h.XPCServices = append(h.XPCServices, strings.TrimSuffix(e.Name(), ".xpc"))
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(appPath, "Contents", "PlugIns")); err == nil {
		for _, e := range entries {
			h.Plugins = append(h.Plugins, e.Name())
		}
	}
	sort.Strings(h.HelperApps)
	sort.Strings(h.XPCServices)
	sort.Strings(h.Plugins)
	if len(h.HelperApps) == 0 && len(h.XPCServices) == 0 && len(h.Plugins) == 0 {
		return nil
	}
	return h
}

// scanLoginItems looks under the three launchd directories for plists
// associated with this bundle. Attribution is by either:
//   - filename / Label prefix matching the bundle's reverse-DNS prefix
//     (first two segments, e.g. com.docker), or
//   - any ProgramArguments path that resides inside the .app bundle.
func scanLoginItems(appPath, bundleID string) []LoginItem {
	prefix := bundleIDPrefix(bundleID)
	var items []LoginItem
	dirs := []struct {
		path   string
		scope  string
		daemon bool
	}{
		{filepath.Join(home(), "Library", "LaunchAgents"), "user", false},
		{"/Library/LaunchAgents", "system", false},
		{"/Library/LaunchDaemons", "system", true},
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".plist") {
				continue
			}
			full := filepath.Join(d.path, e.Name())
			label := strings.TrimSuffix(e.Name(), ".plist")
			matched := false
			if prefix != "" && strings.HasPrefix(label, prefix) {
				matched = true
			} else {
				// Fall back: peek at ProgramArguments / BundleProgram.
				if plistMentionsAppPath(full, appPath) {
					matched = true
				}
			}
			if matched {
				runAtLoad, keepAlive := parsePlistLaunchFlags(full)
				items = append(items, LoginItem{
					Path:      full,
					Label:     label,
					Scope:     d.scope,
					Daemon:    d.daemon,
					RunAtLoad: runAtLoad,
					KeepAlive: keepAlive,
				})
			}
		}
	}
	return items
}

// bundleIDPrefix returns the first two reverse-DNS segments (e.g.
// "com.docker") from a full bundle ID, or "" if not enough segments.
func bundleIDPrefix(bundleID string) string {
	parts := strings.SplitN(bundleID, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// plistMentionsAppPath returns true if the launchd plist's text payload
// includes the app bundle path. We use the converted XML form to keep
// this regex-friendly without requiring a plist parser.
func plistMentionsAppPath(plistPath, appPath string) bool {
	out, err := exec.Command("plutil", "-convert", "xml1", "-o", "-", plistPath).Output()
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte(appPath))
}

// parsePlistXMLBool returns true if the converted XML for plistPath contains
// the given key immediately followed by a <true/> element.
// It reads the plist once and checks both RunAtLoad and KeepAlive so the
// caller gets both flags from a single plutil invocation.
func parsePlistLaunchFlags(plistPath string) (runAtLoad, keepAlive bool) {
	out, err := exec.Command("plutil", "-convert", "xml1", "-o", "-", plistPath).Output()
	if err != nil {
		return
	}
	runAtLoad = plistXMLBool(out, "RunAtLoad")
	keepAlive = plistXMLBool(out, "KeepAlive")
	return
}

// plistXMLBool returns true when the XML payload contains <key>name</key>
// followed (possibly with whitespace) by <true/>.
func plistXMLBool(xml []byte, name string) bool {
	key := []byte("<key>" + name + "</key>")
	idx := bytes.Index(xml, key)
	if idx < 0 {
		return false
	}
	rest := bytes.TrimSpace(xml[idx+len(key):])
	return bytes.HasPrefix(rest, []byte("<true/>"))
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// lstartLayout is the Go parse layout for the 5-token lstart output from ps.
// e.g. "Sat May  2 22:37:01 2026"
const lstartLayout = "Mon Jan  2 15:04:05 2006"

// scanRunningProcesses uses `ps` to find processes whose executable path
// lives inside this bundle. Reports PID, RSS, start time, and a bundle-relative path.
//
// ps format: pid=,rss=,lstart=,comm=
// lstart produces 5 whitespace-separated tokens ("Sat May  2 22:37:01 2026")
// so the command starts at fields[7] (0-indexed: 0=pid, 1=rss, 2-6=lstart, 7+=comm).
func scanRunningProcesses(appPath string) []ProcessInfo {
	out, err := exec.Command("ps", "-axwwo", "pid=,rss=,lstart=,comm=").Output()
	if err != nil {
		return nil
	}
	var procs []ProcessInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// minimum: pid(1) + rss(1) + lstart(5) + comm(1) = 8 fields
		if len(fields) < 8 {
			continue
		}
		// fields[2:7] are the 5 lstart tokens; fields[7:] are the command.
		cmd := strings.Join(fields[7:], " ")
		if !strings.HasPrefix(cmd, appPath+"/") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		rss, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		lstartStr := strings.Join(fields[2:7], " ")
		startTime := parseLstart(lstartStr)

		procs = append(procs, ProcessInfo{
			PID:       pid,
			RSSKiB:    rss,
			Command:   strings.TrimPrefix(cmd, appPath+"/"),
			StartTime: startTime,
		})
	}
	return procs
}

func parseLstart(s string) time.Time {
	t, _ := time.ParseInLocation(lstartLayout, s, time.Local)
	return t
}

func appUptime(procs []ProcessInfo, now time.Time) (*time.Time, int64) {
	var oldest time.Time
	for _, p := range procs {
		if p.StartTime.IsZero() {
			continue
		}
		if oldest.IsZero() || p.StartTime.Before(oldest) {
			oldest = p.StartTime
		}
	}
	if oldest.IsZero() {
		return nil, 0
	}
	seconds := int64(now.Sub(oldest).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	started := oldest
	return &started, seconds
}

// readPrivacyDescriptions returns the NS*UsageDescription keys declared
// in Info.plist along with their human-readable descriptions. These are
// the strings macOS shows in permission prompts.
func readPrivacyDescriptions(appPath string) map[string]string {
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	// Convert to xml1 once; cheaper than one plutil exec per key.
	out, err := exec.Command("plutil", "-convert", "xml1", "-o", "-", plist).Output()
	if err != nil {
		return nil
	}
	xml := string(out)
	// Match <key>NS*UsageDescription</key><string>...</string> pairs.
	re := regexp.MustCompile(`<key>(NS[A-Za-z]+UsageDescription)</key>\s*<string>([^<]*)</string>`)
	matches := re.FindAllStringSubmatch(xml, -1)
	if len(matches) == 0 {
		return nil
	}
	result := make(map[string]string, len(matches))
	for _, m := range matches {
		result[m[1]] = m[2]
	}
	return result
}

// scanDependencies summarises third-party libraries embedded in the bundle.
// Apple frameworks and Helper sub-apps are filtered out.
func scanDependencies(appPath string) *Dependencies {
	return defaultNodeAppInspector().scanDependencies(appPath)
}

func (ins nodeAppInspector) scanDependencies(appPath string) *Dependencies {
	d := &Dependencies{}
	frameworks := filepath.Join(appPath, "Contents", "Frameworks")
	if entries, err := ins.fs.ReadDir(frameworks); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".framework") {
				continue
			}
			base := strings.TrimSuffix(name, ".framework")
			// Skip Helper sub-apps (e.g. "Slack Helper.app") — handled by Electron.
			// Skip the Electron Framework itself; it's already implicit.
			if base == "Electron Framework" {
				continue
			}
			d.ThirdPartyFrameworks = append(d.ThirdPartyFrameworks, base)
		}
		sort.Strings(d.ThirdPartyFrameworks)
	}

	// npm packages: top-level dirs under app.asar.unpacked/node_modules.
	nm := filepath.Join(appPath, "Contents", "Resources", "app.asar.unpacked", "node_modules")
	if entries, err := ins.fs.ReadDir(nm); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasPrefix(n, "@") {
				// Scoped: list each scoped package as @scope/pkg.
				if subs, err := ins.fs.ReadDir(filepath.Join(nm, n)); err == nil {
					for _, s := range subs {
						if s.IsDir() {
							d.NPMPackages = append(d.NPMPackages, n+"/"+s.Name())
						}
					}
				}
				continue
			}
			d.NPMPackages = append(d.NPMPackages, n)
		}
		sort.Strings(d.NPMPackages)
	}

	d.JavaJars = ins.countJars(filepath.Join(appPath, "Contents"))

	if len(d.ThirdPartyFrameworks) == 0 && len(d.NPMPackages) == 0 && d.JavaJars == 0 {
		return nil
	}
	return d
}

// scanNetworkEndpoints harvests distinct hostnames from URL strings found
// in the main executable and app.asar (when present). Cheap-but-effective:
// most apps don't bother to obfuscate URL literals.
func scanNetworkEndpoints(appPath, exe string) []string {
	return defaultNodeAppInspector().scanNetworkEndpoints(appPath, exe)
}

func (ins nodeAppInspector) scanNetworkEndpoints(appPath, exe string) []string {
	hosts := map[string]struct{}{}
	urlRe := regexp.MustCompile(`(?i)https?://([a-zA-Z0-9._-]+\.[a-zA-Z]{2,})`)

	addFrom := func(path string) {
		if path == "" {
			return
		}
		// Stream in chunks; we only care about ASCII URL substrings.
		f, err := ins.fs.Open(path)
		if err != nil {
			return
		}
		defer f.Close()

		buf := make([]byte, 1<<20)
		overlap := 256
		tail := make([]byte, 0, overlap)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				region := append(tail, buf[:n]...)
				for _, m := range urlRe.FindAllSubmatch(region, -1) {
					hosts[strings.ToLower(string(m[1]))] = struct{}{}
				}
				if len(region) > overlap {
					tail = append(tail[:0], region[len(region)-overlap:]...)
				} else {
					tail = append(tail[:0], region...)
				}
			}
			if err != nil {
				break
			}
		}
	}

	addFrom(exe)
	asar := filepath.Join(appPath, "Contents", "Resources", "app.asar")
	if ins.fs.Exists(asar) {
		addFrom(asar)
	}

	// Filter out hostnames that are too generic to be useful (schema URIs).
	junk := map[string]bool{
		"www.w3.org":                 true,
		"www.apple.com":              true,
		"developer.apple.com":        true,
		"schemas.microsoft.com":      true,
		"schemas.openxmlformats.org": true,
		"json-schema.org":            true,
		"www.google.com":             false, // keep — meaningful when present
	}
	out := make([]string, 0, len(hosts))
	for h := range hosts {
		if junk[h] {
			continue
		}
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func rel(base, target string) string {
	if r, err := filepath.Rel(base, target); err == nil {
		return r
	}
	return target
}
