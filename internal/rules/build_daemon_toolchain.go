package rules

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// Bounds keep the project scan cheap and safe on odd trees.
const (
	toolchainMaxDepth    = 12        // directories walked upward from the daemon cwd
	toolchainMaxFileSize = 256 << 10 // bytes; larger files are skipped, not truncated
	toolchainMaxSubdirs  = 50        // immediate subprojects whose build file is read
	toolchainMaxPlugins  = 40        // convention-plugin files read
	toolchainPluginDepth = 4
)

type dirEntry struct {
	Name  string
	IsDir bool
}

// toolchainFS is the filesystem seam for the project scan.
type toolchainFS interface {
	// ReadFile returns at most maxBytes+1 bytes so callers can detect oversize.
	ReadFile(path string, maxBytes int64) ([]byte, error)
	ReadDir(path string) ([]dirEntry, error)
}

type osToolchainFS struct{}

func (osToolchainFS) ReadFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxBytes+1))
}

func (osToolchainFS) ReadDir(path string) ([]dirEntry, error) {
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(ents))
	for _, e := range ents {
		out = append(out, dirEntry{Name: e.Name(), IsDir: e.IsDir()})
	}
	return out, nil
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}

// expectation is one place that says which JDK the project wants.
type expectation struct {
	file  string
	line  int
	text  string
	major int    // 0 when only a path is given
	path  string // org.gradle.java.home value
}

func (x expectation) evidence() string {
	return fmt.Sprintf("%s:%d: %s", x.file, x.line, strings.TrimSpace(x.text))
}

// ruleBuildDaemonJDKMismatch flags a running Gradle or Kotlin daemon whose JDK
// differs from what the project configures. fsys reads project files; home is
// the fallback for the user's ~/.gradle when the daemon has no user.home.
func ruleBuildDaemonJDKMismatch(fsys toolchainFS, home string) Rule {
	return Rule{
		ID:       "build-daemon-jdk-mismatch",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, j := range s.JVMs {
				kind := jvm.BuildDaemonKind(j.MainClass)
				if kind == "" {
					continue
				}
				if f, ok := daemonToolchainFinding(fsys, home, j, kind); ok {
					findings = append(findings, f)
				}
			}
			return findings
		},
	}
}

func daemonToolchainFinding(fsys toolchainFS, home string, j jvm.Info, kind string) (Finding, bool) {
	cwd := j.SysProps["user.dir"]
	if cwd == "" {
		return Finding{}, false
	}
	if h := j.SysProps["user.home"]; h != "" {
		home = h
	}
	have := daemonMajor(fsys, j)
	if have == 0 {
		return Finding{}, false
	}
	pins, toolchain := scanProject(fsys, home, cwd)
	fix := fmt.Sprintf("Stop just this daemon with `kill %d` (or let it idle out) so the next build starts one on the expected JDK. Do not run `gradle --stop`: it stops every compatible daemon on the host. See `spectra jvm explain %d`.", j.PID, j.PID)
	mk := func(sev Severity, msg string, ev []expectation) Finding {
		parts := make([]string, len(ev))
		for i, e := range ev {
			parts[i] = e.evidence()
		}
		return Finding{
			RuleID:   "build-daemon-jdk-mismatch",
			Severity: sev,
			Subject:  fmt.Sprintf("PID %d (%s)", j.PID, j.MainClass),
			Message:  msg + " Evidence: " + strings.Join(parts, "; "),
			Fix:      fix,
		}
	}
	if len(pins) > 0 {
		p := pins[0]
		if pinMatches(fsys, p, j, have) {
			return Finding{}, false
		}
		want := fmt.Sprintf("JDK %d", p.major)
		if p.major == 0 {
			want = fmt.Sprintf("java.home %q", p.path)
		}
		return mk(SeverityMedium, fmt.Sprintf("%s runs on JDK %d (java.home=%q) but the project pins %s.", kind, have, j.JavaHome, want), []expectation{p}), true
	}
	if len(toolchain) == 0 {
		return Finding{}, false
	}
	for _, t := range toolchain {
		if t.major == have {
			return Finding{}, false
		}
	}
	return mk(SeverityLow, fmt.Sprintf("%s runs on JDK %d, which differs from the compile toolchain (usually fine: toolchains compile on a separate JDK).", kind, have), toolchain), true
}

func pinMatches(fsys toolchainFS, p expectation, j jvm.Info, have int) bool {
	if p.major != 0 {
		return p.major == have
	}
	if filepath.Clean(p.path) == filepath.Clean(j.JavaHome) {
		return true
	}
	m := releaseMajor(fsys, p.path)
	return m != 0 && m == have
}

func daemonMajor(fsys toolchainFS, j jvm.Info) int {
	if m := parseJavaMajor(j.JDKVersion); m != 0 {
		return m
	}
	if j.JavaHome != "" {
		return releaseMajor(fsys, j.JavaHome)
	}
	return 0
}

var releaseRe = regexp.MustCompile(`(?m)^JAVA_VERSION="?([^"\r\n]+)"?`)

func releaseMajor(fsys toolchainFS, javaHome string) int {
	b, err := fsys.ReadFile(filepath.Join(javaHome, "release"), toolchainMaxFileSize)
	if err != nil || int64(len(b)) > toolchainMaxFileSize {
		return 0
	}
	if m := releaseRe.FindSubmatch(b); m != nil {
		return parseJavaMajor(string(m[1]))
	}
	return 0
}

var versionRe = regexp.MustCompile(`(\d+)(?:\.(\d+))?`)

// parseJavaMajor extracts the Java major version from strings such as "17.0.9",
// "1.8.0_392", "temurin-21.0.2", or "17.0.9-tem". Zero means unparseable.
func parseJavaMajor(v string) int {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	if n == 1 && m[2] != "" {
		n, _ = strconv.Atoi(m[2])
	}
	if n < 1 || n > 99 {
		return 0
	}
	return n
}

// scanProject walks from cwd upward to the first directory holding
// settings.gradle(.kts), collecting pins (in precedence order) and toolchain
// blocks (only when a project root was found).
func scanProject(fsys toolchainFS, home, cwd string) (pins, toolchain []expectation) {
	read := func(p string) ([]byte, bool) {
		b, err := fsys.ReadFile(p, toolchainMaxFileSize)
		if err != nil || int64(len(b)) > toolchainMaxFileSize {
			return nil, false
		}
		return b, true
	}
	// The user-level gradle.properties overrides the project's.
	if home != "" {
		f := filepath.Join(home, ".gradle", "gradle.properties")
		if b, ok := read(f); ok {
			if x, ok := javaHomeProp(f, b); ok {
				pins = append(pins, x)
			}
		}
	}
	var dirs []string
	root := ""
	dir := filepath.Clean(cwd)
	for i := 0; i < toolchainMaxDepth; i++ {
		dirs = append(dirs, dir)
		if hasSettings(fsys, dir) {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	var props, java, tool, sdk []expectation
	for _, d := range dirs {
		if f := filepath.Join(d, "gradle.properties"); fileOK(read, f) {
			if x, ok := javaHomeProp(f, mustRead(read, f)); ok {
				props = append(props, x)
			}
		}
		java = append(java, versionFile(read, filepath.Join(d, ".java-version"), "")...)
		tool = append(tool, versionFile(read, filepath.Join(d, ".tool-versions"), "java")...)
		sdk = append(sdk, versionFile(read, filepath.Join(d, ".sdkmanrc"), "java=")...)
	}
	pins = append(pins, props...)
	pins = append(pins, java...)
	pins = append(pins, tool...)
	pins = append(pins, sdk...)
	if root != "" {
		toolchain = scanToolchainBlocks(fsys, root, read)
	}
	return pins, toolchain
}

func hasSettings(fsys toolchainFS, dir string) bool {
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir && (e.Name == "settings.gradle" || e.Name == "settings.gradle.kts") {
			return true
		}
	}
	return false
}

func splitLines(b []byte) []string { return strings.Split(string(b), "\n") }

func javaHomeProp(file string, b []byte) (expectation, bool) {
	for i, l := range splitLines(b) {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "!") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok || strings.TrimSpace(k) != "org.gradle.java.home" {
			continue
		}
		if v = strings.TrimSpace(v); v != "" {
			return expectation{file: file, line: i + 1, text: t, path: v}, true
		}
	}
	return expectation{}, false
}

// versionFileExpectation parses .java-version (key ""), .tool-versions (key
// "java", whitespace separated) and .sdkmanrc (key "java=").
func versionFileExpectation(file string, b []byte, key string) []expectation {
	for i, l := range splitLines(b) {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		val := t
		switch key {
		case "java":
			f := strings.Fields(t)
			if len(f) < 2 || f[0] != "java" {
				continue
			}
			val = f[1]
		case "java=":
			k, v, ok := strings.Cut(t, "=")
			if !ok || strings.TrimSpace(k) != "java" {
				continue
			}
			val = strings.TrimSpace(v)
		}
		if m := parseJavaMajor(val); m != 0 {
			return []expectation{{file: file, line: i + 1, text: t, major: m}}
		}
		if key == "" {
			return nil // .java-version holds a single value
		}
	}
	return nil
}

var (
	langVersionRe = regexp.MustCompile(`languageVersion\s*(?:=|\.set\()\s*JavaLanguageVersion\.of\(\s*(\d+)\s*\)`)
	jvmToolchain  = regexp.MustCompile(`jvmToolchain\s*\(\s*(\d+)\s*\)`)
	javaVersionRe = regexp.MustCompile(`JavaVersion\.VERSION_(\d+(?:_\d+)?)`)
	toolchainOpen = regexp.MustCompile(`\btoolchain\b[^{]*\{`)
)

func scanToolchainBlocks(fsys toolchainFS, root string, read func(string) ([]byte, bool)) []expectation {
	var files []string
	for _, n := range []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"} {
		files = append(files, filepath.Join(root, n))
	}
	if ents, err := fsys.ReadDir(root); err == nil {
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
		n := 0
		for _, e := range ents {
			if !e.IsDir || strings.HasPrefix(e.Name, ".") || e.Name == "build" || e.Name == "node_modules" || e.Name == "buildSrc" || e.Name == "build-logic" {
				continue
			}
			if n++; n > toolchainMaxSubdirs {
				break
			}
			files = append(files, filepath.Join(root, e.Name, "build.gradle"), filepath.Join(root, e.Name, "build.gradle.kts"))
		}
	}
	for _, d := range []string{"buildSrc", "build-logic"} {
		count := 0
		collectPluginFiles(fsys, filepath.Join(root, d), 0, &count, &files)
	}
	var out []expectation
	for _, f := range files {
		if b, ok := read(f); ok {
			out = append(out, toolchainMatches(f, b)...)
		}
	}
	return out
}

func collectPluginFiles(fsys toolchainFS, dir string, depth int, count *int, out *[]string) {
	if depth > toolchainPluginDepth || *count >= toolchainMaxPlugins {
		return
	}
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		return
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
	for _, e := range ents {
		if *count >= toolchainMaxPlugins {
			return
		}
		p := filepath.Join(dir, e.Name)
		if e.IsDir {
			if e.Name != "build" && !strings.HasPrefix(e.Name, ".") {
				collectPluginFiles(fsys, p, depth+1, count, out)
			}
			continue
		}
		if strings.HasSuffix(e.Name, ".gradle") || strings.HasSuffix(e.Name, ".gradle.kts") || strings.HasSuffix(e.Name, ".kt") {
			*count++
			*out = append(*out, p)
		}
	}
}

// toolchainMatches finds toolchain version requests in one build file.
// JavaVersion.VERSION_N counts only inside a `toolchain { ... }` block.
func toolchainMatches(file string, b []byte) []expectation {
	var out []expectation
	depth := 0 // brace depth inside a toolchain block; 0 means outside
	for i, l := range splitLines(b) {
		line := l
		add := func(n string) {
			if m := parseJavaMajor(strings.ReplaceAll(n, "_", ".")); m != 0 {
				out = append(out, expectation{file: file, line: i + 1, text: line, major: m})
			}
		}
		if m := langVersionRe.FindStringSubmatch(l); m != nil {
			add(m[1])
		} else if m := jvmToolchain.FindStringSubmatch(l); m != nil {
			add(m[1])
		} else if m := javaVersionRe.FindStringSubmatch(l); m != nil && depth > 0 {
			add(m[1])
		}
		if depth == 0 {
			loc := toolchainOpen.FindStringIndex(l)
			if loc == nil {
				continue
			}
			depth = 1
			l = l[loc[1]:]
		}
		depth += strings.Count(l, "{") - strings.Count(l, "}")
		if depth < 0 {
			depth = 0
		}
	}
	return out
}

func fileOK(read func(string) ([]byte, bool), f string) bool { _, ok := read(f); return ok }

func mustRead(read func(string) ([]byte, bool), f string) []byte { b, _ := read(f); return b }

func versionFile(read func(string) ([]byte, bool), f, key string) []expectation {
	b, ok := read(f)
	if !ok {
		return nil
	}
	return versionFileExpectation(f, b, key)
}
