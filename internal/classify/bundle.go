package classify

import (
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/plistread"
)

// bundleLoc is where an executable sits relative to its outermost .app.
type bundleLoc struct {
	root string // outermost .app path; empty when the executable is not in one
	// frameworkInternal is set when a .framework precedes the .app in the path
	// (Homebrew's Python.framework/.../Python.app): a launcher stub, not an app.
	frameworkInternal bool
	// developer is set for executables under <app>/Contents/Developer/ (Xcode's
	// toolchain): command-line tools that happen to ship inside an app.
	developer bool
}

// appOwned reports whether the bundle makes the process an app or app helper.
func (l bundleLoc) appOwned() bool {
	return l.root != "" && !l.frameworkInternal && !l.developer
}

func locateBundle(exe string) bundleLoc {
	if !filepath.IsAbs(exe) {
		return bundleLoc{}
	}
	parts := strings.Split(exe, "/")
	var loc bundleLoc
	for i, part := range parts[:len(parts)-1] {
		if strings.HasSuffix(part, ".framework") {
			loc.frameworkInternal = true
		}
		if strings.HasSuffix(part, ".app") {
			loc.root = strings.Join(parts[:i+1], "/")
			loc.developer = strings.HasPrefix(exe, loc.root+"/Contents/Developer/")
			return loc
		}
	}
	return bundleLoc{}
}

// bundleInfo is what the classifier needs from one .app, read once per run.
type bundleInfo struct {
	name     string
	bundleID string
	mainExe  string // absolute path of CFBundleExecutable; empty when unknown
	electron bool
	// jvm is the bundled Java runtime directory when the app embeds one
	// (JetBrains' Contents/jbr, jpackage's Contents/runtime).
	jvm string
}

// maxPlistBytes caps one Info.plist read; real ones are a few KiB.
const maxPlistBytes = 1 << 20

func readBundle(files FileSystem, root string) bundleInfo {
	info := bundleInfo{name: strings.TrimSuffix(filepath.Base(root), ".app")}
	contents := filepath.Join(root, "Contents")
	if data, err := files.ReadFile(filepath.Join(contents, "Info.plist"), maxPlistBytes); err == nil {
		if v, err := plistread.Decode(data); err == nil {
			info.bundleID = plistString(v, "CFBundleIdentifier")
			if exe := plistString(v, "CFBundleExecutable"); exe != "" && !strings.Contains(exe, "/") {
				info.mainExe = filepath.Join(contents, "MacOS", exe)
			}
		}
	}
	if _, err := files.Stat(filepath.Join(contents, "Frameworks", "Electron Framework.framework")); err == nil {
		info.electron = true
	}
	for _, rel := range []string{"jbr/Contents/Home", "runtime/Contents/Home"} {
		dir := filepath.Join(contents, rel)
		if _, err := files.Stat(filepath.Join(dir, "bin", "java")); err == nil {
			info.jvm = dir
			break
		}
	}
	return info
}

func plistString(v plistread.Value, key string) string {
	if got, ok := v.Lookup(key); ok && got.Kind == plistread.String {
		return got.String
	}
	return ""
}

// isMainExecutable reports whether exe is the bundle's CFBundleExecutable.
// Without a readable Info.plist any executable directly in Contents/MacOS
// counts as main.
func (b bundleInfo) isMainExecutable(root, exe string) bool {
	if b.mainExe != "" {
		return exe == b.mainExe
	}
	return filepath.Dir(exe) == filepath.Join(root, "Contents", "MacOS")
}
