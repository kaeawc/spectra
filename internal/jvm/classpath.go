package jvm

import (
	"path/filepath"
	"regexp"
	"strings"
)

// LaunchClassPath is the class path a `java` command line asks for.
// Entries are as written: possibly relative, possibly "dir/*" wildcards.
type LaunchClassPath struct {
	// Jar is the -jar argument; when set, the JVM ignores -cp and the class
	// path is Jar plus its manifest's Class-Path.
	Jar       string
	ClassPath []string
	// Explicit is false when the command line names no class path, in which
	// case the JVM uses $CLASSPATH or ".".
	Explicit bool
	// Unknown is set when the class path cannot be read from argv: it may
	// sit in an @argfile, or -jar has no value.
	Unknown bool
}

// NeedsCwd reports whether resolving the class path needs the process's
// working directory.
func (cp LaunchClassPath) NeedsCwd() bool {
	if cp.Jar != "" {
		return !filepath.IsAbs(cp.Jar)
	}
	if !cp.Explicit {
		return true
	}
	for _, e := range cp.ClassPath {
		if !filepath.IsAbs(e) {
			return true
		}
	}
	return false
}

// javaOptsWithValue are launcher options whose value is the next argument,
// so it is not mistaken for the main class.
var javaOptsWithValue = map[string]bool{
	"-cp": true, "-classpath": true, "--class-path": true,
	"-p": true, "--module-path": true, "--upgrade-module-path": true,
	"--add-modules": true, "--limit-modules": true, "--add-reads": true,
	"--add-exports": true, "--add-opens": true, "--patch-module": true,
	"--enable-native-access": true, "--source": true,
}

// ParseLaunchArgs reads the class path from a java argv. Parsing stops at
// the main class, -jar, -m/--module, or an @argfile (whose contents are not
// read, so the class path is unknown). A native launcher that names no
// class path (an IDE's own binary) sets it through JNI, so it is unknown
// too rather than assumed to be ".".
func ParseLaunchArgs(argv []string) LaunchClassPath {
	if len(argv) == 0 {
		return LaunchClassPath{Unknown: true}
	}
	lcp := parseLaunchArgs(argv)
	if !lcp.Explicit && !lcp.Unknown && !isJavaLauncher(argv[0]) {
		lcp.Unknown = true
	}
	return lcp
}

func isJavaLauncher(arg0 string) bool {
	switch strings.ToLower(filepath.Base(arg0)) {
	case "java", "javaw", "java.exe", "javaw.exe":
		return true
	}
	return false
}

func parseLaunchArgs(argv []string) LaunchClassPath {
	var lcp LaunchClassPath
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-jar":
			if i+1 < len(argv) {
				return LaunchClassPath{Jar: argv[i+1], Explicit: true}
			}
			return LaunchClassPath{Unknown: true}
		case a == "-cp" || a == "-classpath" || a == "--class-path":
			if i+1 < len(argv) {
				lcp.ClassPath, lcp.Explicit = splitClassPath(argv[i+1]), true
			}
			i++
		case strings.HasPrefix(a, "--class-path="):
			lcp.ClassPath, lcp.Explicit = splitClassPath(strings.TrimPrefix(a, "--class-path=")), true
		case strings.HasPrefix(a, "@"):
			return LaunchClassPath{Unknown: true}
		case a == "-m" || a == "--module" || strings.HasPrefix(a, "--module="):
			return lcp
		case javaOptsWithValue[a]:
			i++
		case !strings.HasPrefix(a, "-"):
			return lcp
		}
	}
	return lcp
}

func splitClassPath(v string) []string {
	var out []string
	for _, e := range filepath.SplitList(v) {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// frameClassRE captures the qualified method of an "at ..." frame, after
// any "loader/module@version/" prefix.
var frameClassRE = regexp.MustCompile(`^at\s+(\S+?)\.[^.(]+\(`)

// FrameClass returns the binary class name of a thread-dump frame such as
// "at app//com.example.Foo$Bar.run(Foo.java:12)". Hidden classes (lambdas,
// generated proxies) never live in a jar or directory and return false.
func FrameClass(frame string) (string, bool) {
	m := frameClassRE.FindStringSubmatch(strings.TrimSpace(frame))
	if m == nil {
		return "", false
	}
	qualified := m[1]
	if strings.Contains(qualified, "$$Lambda") || strings.Contains(qualified, "/0x") {
		return "", false
	}
	if i := strings.LastIndex(qualified, "/"); i >= 0 {
		qualified = qualified[i+1:]
	}
	if qualified == "" || strings.HasPrefix(qualified, ".") {
		return "", false
	}
	return qualified, true
}

// classResource is the path of a class file inside a jar or class dir.
func classResource(class string) string {
	return strings.ReplaceAll(class, ".", "/") + ".class"
}
