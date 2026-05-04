package toolchain

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// discoverJDKs enumerates all installed JDKs across every well-known
// installation location. Parses each JDK's `release` file for version +
// vendor rather than running `java -version` (faster; works for broken JDKs).
func discoverJDKs(opts CollectOptions) ([]JDKInstall, error) {
	javaHome := os.Getenv("JAVA_HOME")

	type searchRoot struct {
		dir    string
		source string
		sub    string // subdirectory inside each match to find JDK home, e.g. "Contents/Home"
	}

	roots := []searchRoot{
		{opts.SystemJVMRoot, "system", "Contents/Home"},
		{opts.UserJVMRoot, "manual", "Contents/Home"},
		{filepath.Join(opts.Home, ".sdkman", "candidates", "java"), "sdkman", ""},
		{filepath.Join(opts.Home, ".asdf", "installs", "java"), "asdf", ""},
		{filepath.Join(opts.Home, ".local", "share", "mise", "installs", "java"), "mise", ""},
		{filepath.Join(opts.Home, "Library", "Caches", "Coursier", "jvm"), "coursier", ""},
	}
	// Homebrew installs openjdk@* as symlinks in opt/
	for _, cellar := range opts.BrewCellars {
		base := filepath.Dir(cellar) // /opt/homebrew or /usr/local
		roots = append(roots, searchRoot{filepath.Join(base, "opt"), "brew", ""})
	}
	// JetBrains Toolbox JBRs
	tbApps := filepath.Join(opts.Home, "Library", "Application Support", "JetBrains", "Toolbox", "apps")
	roots = append(roots, searchRoot{tbApps, "jbr-toolbox", "jbr/Contents/Home"})

	seen := map[string]bool{}
	var out []JDKInstall
	for _, r := range roots {
		entries, err := readDir(r.dir)
		if err != nil {
			continue
		}
		for _, name := range entries {
			candidate := filepath.Join(r.dir, name)
			// For brew opt/, only accept openjdk* symlinks.
			if r.source == "brew" && !strings.HasPrefix(name, "openjdk") {
				continue
			}
			home := candidate
			if r.sub != "" {
				home = filepath.Join(candidate, r.sub)
			}
			if seen[home] {
				continue
			}
			jdk, ok := parseJDKHome(home, r.source)
			if !ok {
				continue
			}
			jdk.Path = home
			jdk.IsActiveJavaHome = (javaHome != "" && strings.HasPrefix(home, javaHome))
			jdk.InstallID = fmt.Sprintf("%s-%s-%d.%d.%d",
				jdk.Source, sanitize(jdk.Vendor), jdk.VersionMajor, jdk.VersionMinor, jdk.VersionPatch)
			seen[home] = true
			out = append(out, jdk)
		}
	}
	return out, nil
}

// parseJDKHome reads <jdkHome>/release and returns a populated JDKInstall.
func parseJDKHome(jdkHome, source string) (JDKInstall, bool) {
	f, err := os.Open(filepath.Join(jdkHome, "release"))
	if err != nil {
		return JDKInstall{}, false
	}
	defer f.Close()

	props := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}

	raw := props["JAVA_VERSION"]
	if raw == "" {
		raw = props["JAVA_RUNTIME_VERSION"]
	}
	if raw == "" {
		return JDKInstall{}, false
	}

	jdk := JDKInstall{
		Source:        source,
		ReleaseString: props["JAVA_RUNTIME_VERSION"],
		Vendor:        props["IMPLEMENTOR"],
	}
	// Parse "21.0.6", "1.8.0_392", "21.0.6+7-LTS"
	clean := strings.FieldsFunc(raw, func(r rune) bool { return r == '+' || r == '-' })[0]
	parts := strings.Split(clean, ".")
	if len(parts) >= 1 {
		major, _ := strconv.Atoi(parts[0])
		// Old JDK "1.8.0_N" → major is parts[1]
		if major == 1 && len(parts) >= 2 {
			major, _ = strconv.Atoi(parts[1])
		}
		jdk.VersionMajor = major
	}
	if len(parts) >= 2 {
		minor, _ := strconv.Atoi(parts[1])
		jdk.VersionMinor = minor
	}
	if len(parts) >= 3 {
		patch, _ := strconv.Atoi(strings.FieldsFunc(parts[2], func(r rune) bool { return r == '_' })[0])
		jdk.VersionPatch = patch
	}
	return jdk, true
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, s)
}

// readDir lists entries in dir, returning names only. Silently ignores errors.
func readDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
