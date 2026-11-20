package rules

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

type fakeFS struct {
	files map[string]string
	dirs  map[string][]dirEntry
}

func newFakeFS(files map[string]string) *fakeFS {
	f := &fakeFS{files: files, dirs: map[string][]dirEntry{}}
	for p := range files {
		d, name := filepath.Dir(p), filepath.Base(p)
		f.dirs[d] = append(f.dirs[d], dirEntry{Name: name})
		for ; d != "/" && d != "."; d = filepath.Dir(d) {
			parent := filepath.Dir(d)
			dup := false
			for _, e := range f.dirs[parent] {
				if e.Name == filepath.Base(d) {
					dup = true
				}
			}
			if !dup {
				f.dirs[parent] = append(f.dirs[parent], dirEntry{Name: filepath.Base(d), IsDir: true})
			}
		}
	}
	for _, v := range f.dirs {
		sort.Slice(v, func(i, j int) bool { return v[i].Name < v[j].Name })
	}
	return f
}

func (f *fakeFS) ReadFile(p string, max int64) ([]byte, error) {
	s, ok := f.files[p]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	if int64(len(s)) > max+1 {
		s = s[:max+1]
	}
	return []byte(s), nil
}

func (f *fakeFS) ReadDir(p string) ([]dirEntry, error) {
	d, ok := f.dirs[p]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return d, nil
}

const gradleMain = "org.gradle.launcher.daemon.bootstrap.GradleDaemon"

func daemon(version, cwd string) jvm.Info {
	return jvm.Info{PID: 42, MainClass: gradleMain, JavaHome: "/jdks/" + version, JDKVersion: version,
		SysProps: map[string]string{"user.dir": cwd, "user.home": "/home/u"}}
}

func run(t *testing.T, files map[string]string, j jvm.Info) []Finding {
	t.Helper()
	s := baseSnap()
	s.JVMs = []jvm.Info{j}
	return ruleBuildDaemonJDKMismatch(newFakeFS(files), "/home/u").MatchFn(s)
}

func TestBuildDaemonToolchainPinFiles(t *testing.T) {
	const proj = "/work/app"
	settings := map[string]string{proj + "/settings.gradle.kts": ""}
	cases := []struct {
		name, file, body, wantLine string
	}{
		{"gradle.properties", proj + "/gradle.properties", "# c\norg.gradle.java.home=/jdks/11\n", "gradle.properties:2"},
		{"java-version", proj + "/.java-version", "11\n", ".java-version:1"},
		{"tool-versions", proj + "/.tool-versions", "nodejs 20\njava temurin-11.0.2\n", ".tool-versions:2"},
		{"sdkmanrc", proj + "/.sdkmanrc", "# x\njava=11.0.2-tem\n", ".sdkmanrc:2"},
		{"user gradle.properties", "/home/u/.gradle/gradle.properties", "org.gradle.java.home=/jdks/11\n", "gradle.properties:1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{c.file: c.body}
			for k, v := range settings {
				files[k] = v
			}
			got := run(t, files, daemon("17.0.9", proj))
			if len(got) != 1 || got[0].Severity != SeverityMedium {
				t.Fatalf("want one medium finding, got %+v", got)
			}
			if !strings.Contains(got[0].Message, c.wantLine) || !strings.Contains(got[0].Fix, "kill 42") || !strings.Contains(got[0].Fix, "Do not run `gradle --stop`") {
				t.Errorf("finding = %+v", got[0])
			}
		})
	}
}

func TestBuildDaemonToolchainMatchingVersionNoFinding(t *testing.T) {
	files := map[string]string{
		"/w/settings.gradle":   "",
		"/w/.java-version":     "17\n",
		"/w/.tool-versions":    "java corretto-17.0.1\n",
		"/w/build.gradle.kts":  "kotlin { jvmToolchain(17) }\n",
		"/w/gradle.properties": "org.gradle.java.home=/jdks/17.0.9\n",
	}
	if got := run(t, files, daemon("17.0.9", "/w")); len(got) != 0 {
		t.Fatalf("want none, got %+v", got)
	}
}

func TestBuildDaemonToolchainNoProjectFiles(t *testing.T) {
	files := map[string]string{"/w/README": "x"}
	if got := run(t, files, daemon("17.0.9", "/w/sub")); len(got) != 0 {
		t.Fatalf("want none, got %+v", got)
	}
	// No cwd: nothing to read.
	if got := run(t, files, daemon("17.0.9", "")); len(got) != 0 {
		t.Fatalf("want none, got %+v", got)
	}
}

func TestBuildDaemonToolchainPinPathSameMajorViaRelease(t *testing.T) {
	files := map[string]string{
		"/w/settings.gradle":   "",
		"/w/gradle.properties": "org.gradle.java.home=/link/jdk\n",
		"/link/jdk/release":    "JAVA_VERSION=\"17.0.1\"\n",
	}
	if got := run(t, files, daemon("17.0.9", "/w")); len(got) != 0 {
		t.Fatalf("same major through a different path should not fire, got %+v", got)
	}
	files["/link/jdk/release"] = "JAVA_VERSION=\"21.0.1\"\n"
	if got := run(t, files, daemon("17.0.9", "/w")); len(got) != 1 || got[0].Severity != SeverityMedium {
		t.Fatalf("want medium, got %+v", got)
	}
}

func TestBuildDaemonToolchainOnlyIsLow(t *testing.T) {
	files := map[string]string{
		"/w/settings.gradle.kts":                      "",
		"/w/app/build.gradle.kts":                     "java {\n  toolchain {\n    languageVersion.set(JavaLanguageVersion.of(21))\n  }\n}\n",
		"/w/buildSrc/src/main/kotlin/conv.gradle.kts": "kotlin { jvmToolchain(21) }\n",
	}
	got := run(t, files, daemon("17.0.9", "/w/app"))
	if len(got) != 1 || got[0].Severity != SeverityLow {
		t.Fatalf("want one low finding, got %+v", got)
	}
	m := got[0].Message
	if !strings.Contains(m, "usually fine") || !strings.Contains(m, "app/build.gradle.kts:3") || !strings.Contains(m, "conv.gradle.kts:1") {
		t.Errorf("message = %q", m)
	}
}

func TestBuildDaemonToolchainBlockForms(t *testing.T) {
	cases := map[string]string{
		"assign":       "java.toolchain.languageVersion = JavaLanguageVersion.of(21)\n",
		"set":          "toolchain { languageVersion.set(JavaLanguageVersion.of(21)) }\n",
		"jvmToolchain": "kotlin {\n jvmToolchain(21)\n}\n",
		"javaVersion":  "java {\n toolchain {\n  languageVersion = JavaLanguageVersion.of(21)\n }\n}\n",
		"versionInTC":  "toolchain {\n  sourceCompat = JavaVersion.VERSION_21\n}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"/w/settings.gradle": "", "/w/build.gradle": body}
			got := run(t, files, daemon("17.0.9", "/w"))
			if len(got) != 1 || got[0].Severity != SeverityLow {
				t.Fatalf("got %+v", got)
			}
		})
	}
	// JavaVersion.VERSION_N outside a toolchain block is not evidence.
	files := map[string]string{"/w/settings.gradle": "", "/w/build.gradle": "sourceCompatibility = JavaVersion.VERSION_21\n"}
	if got := run(t, files, daemon("17.0.9", "/w")); len(got) != 0 {
		t.Fatalf("want none, got %+v", got)
	}
}

func TestBuildDaemonToolchainPrecedence(t *testing.T) {
	files := map[string]string{
		"/w/settings.gradle":                "",
		"/w/build.gradle":                   "kotlin { jvmToolchain(21) }\n",
		"/w/.sdkmanrc":                      "java=8.0.1-tem\n",
		"/w/.tool-versions":                 "java 11\n",
		"/w/.java-version":                  "1.8\n",
		"/w/gradle.properties":              "org.gradle.java.home=/jdks/21\n",
		"/home/u/.gradle/gradle.properties": "org.gradle.java.home=/jdks/17.0.9\n",
	}
	// User-level property matches the daemon, so it wins over everything else.
	if got := run(t, files, daemon("17.0.9", "/w")); len(got) != 0 {
		t.Fatalf("user gradle.properties should win, got %+v", got)
	}
	delete(files, "/home/u/.gradle/gradle.properties")
	got := run(t, files, daemon("17.0.9", "/w"))
	if len(got) != 1 || !strings.Contains(got[0].Message, "gradle.properties:1") {
		t.Fatalf("project gradle.properties should win, got %+v", got)
	}
	delete(files, "/w/gradle.properties")
	got = run(t, files, daemon("17.0.9", "/w"))
	if len(got) != 1 || !strings.Contains(got[0].Message, ".java-version:1") || !strings.Contains(got[0].Message, "pins JDK 8") {
		t.Fatalf(".java-version should win next (1.8 => 8), got %+v", got)
	}
	delete(files, "/w/.java-version")
	got = run(t, files, daemon("17.0.9", "/w"))
	if len(got) != 1 || !strings.Contains(got[0].Message, ".tool-versions:1") {
		t.Fatalf(".tool-versions next, got %+v", got)
	}
}

func TestBuildDaemonToolchainWalksUpToSettings(t *testing.T) {
	files := map[string]string{
		"/r/settings.gradle":   "",
		"/r/.java-version":     "11\n",
		"/above/.java-version": "8\n", // beyond the project root: ignored
	}
	got := run(t, files, daemon("17.0.9", "/r/a/b"))
	if len(got) != 1 || !strings.Contains(got[0].Message, "/r/.java-version") {
		t.Fatalf("got %+v", got)
	}
}

func TestBuildDaemonToolchainBounds(t *testing.T) {
	big := "11\n" + strings.Repeat("#", toolchainMaxFileSize+10)
	files := map[string]string{"/w/settings.gradle": "", "/w/.java-version": big}
	if got := run(t, files, daemon("17.0.9", "/w")); len(got) != 0 {
		t.Fatalf("oversized file must be skipped, got %+v", got)
	}

	// A pin deeper than the depth bound is never reached.
	cwd := "/p" + strings.Repeat("/d", toolchainMaxDepth+2)
	files = map[string]string{"/p/.java-version": "11\n", cwd + "/x": ""}
	if got := run(t, files, daemon("17.0.9", cwd)); len(got) != 0 {
		t.Fatalf("depth bound exceeded, got %+v", got)
	}
}

func TestBuildDaemonToolchainIgnoresOtherJVMsAndUnknownVersion(t *testing.T) {
	files := map[string]string{"/w/settings.gradle": "", "/w/.java-version": "11\n"}
	s := baseSnap()
	other := daemon("17.0.9", "/w")
	other.MainClass = "svc.App"
	unknown := daemon("", "/w")
	unknown.JavaHome = ""
	s.JVMs = []jvm.Info{other, unknown}
	if got := ruleBuildDaemonJDKMismatch(newFakeFS(files), "/home/u").MatchFn(s); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	// Version falls back to java.home/release.
	files["/jdks/x/release"] = "JAVA_VERSION=\"17.0.9\"\n"
	j := daemon("", "/w")
	j.JavaHome = "/jdks/x"
	if got := run(t, files, j); len(got) != 1 {
		t.Fatalf("release fallback, got %+v", got)
	}
}

func TestParseJavaMajor(t *testing.T) {
	for in, want := range map[string]int{"17.0.9": 17, "1.8.0_392": 8, "21": 21, "temurin-21.0.2": 21, "17.0.9-tem": 17, "system": 0, "": 0} {
		if got := parseJavaMajor(in); got != want {
			t.Errorf("parseJavaMajor(%q) = %d, want %d", in, got, want)
		}
	}
}
