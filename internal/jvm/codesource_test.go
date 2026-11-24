package jvm

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJar builds a jar at path holding the named entries (empty bodies)
// and, when manifest is non-empty, a META-INF/MANIFEST.MF.
func writeJar(t *testing.T, path, manifest string, entries ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	if manifest != "" {
		w, err := zw.Create("META-INF/MANIFEST.MF")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range entries {
		if _, err := zw.Create(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeClassFile(t *testing.T, dir, resource string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(resource))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte{0xca, 0xfe, 0xba, 0xbe}, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// countingFS records how many times each jar is opened.
type countingFS struct {
	OSCodeSourceFS
	opens map[string]int
}

func (c *countingFS) Open(name string) (ReadAtCloser, error) {
	c.opens[name]++
	return c.OSCodeSourceFS.Open(name)
}

func newTestResolver() (*CodeSourceResolver, *countingFS) {
	fsys := &countingFS{opens: map[string]int{}}
	return &CodeSourceResolver{FS: fsys, Limits: DefaultCodeSourceLimits, Now: time.Now}, fsys
}

func TestResolveClassPath(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app.jar")
	writeJar(t, app, "", "com/example/A.class", "com/example/Shadowed.class", "com/example/A$Inner.class")
	dep := filepath.Join(root, "lib", "dep.jar")
	writeJar(t, dep, "", "org/dep/D.class")
	writeJar(t, filepath.Join(root, "lib", "notes.txt"), "") // not a jar: ignored by the wildcard
	classes := filepath.Join(root, "classes")
	bFile := writeClassFile(t, classes, "com/example/B.class")
	writeClassFile(t, classes, "com/example/Shadowed.class")

	r, fsys := newTestResolver()
	cp := LaunchClassPath{Explicit: true, ClassPath: []string{"classes", "app.jar", "lib/*", "missing.jar"}}
	got := r.Resolve(context.Background(), cp, root, []string{
		"com.example.A", "com.example.A$Inner", "com.example.B", "org.dep.D",
		"com.example.Shadowed", "com.example.Missing", "java.lang.Thread",
	})
	want := map[string]CodeSource{
		"com.example.A":        {Jar: app, JarEntry: "com/example/A.class"},
		"com.example.A$Inner":  {Jar: app, JarEntry: "com/example/A$Inner.class"},
		"com.example.B":        {ClassDir: classes, ClassFile: bFile},
		"org.dep.D":            {Jar: dep, JarEntry: "org/dep/D.class"},
		"com.example.Shadowed": {ClassDir: classes, ClassFile: filepath.Join(classes, "com", "example", "Shadowed.class")},
	}
	if len(got) != len(want) {
		t.Errorf("resolved %d classes, want %d: %+v", len(got), len(want), got)
	}
	for class, w := range want {
		if got[class] != w {
			t.Errorf("%s = %+v, want %+v", class, got[class], w)
		}
	}
	if fsys.opens[app] != 1 || fsys.opens[dep] != 1 {
		t.Errorf("each jar should be indexed once, opens = %v", fsys.opens)
	}

	// A second dump reuses the cached index while the jar is unchanged.
	r.Resolve(context.Background(), cp, root, []string{"com.example.A"})
	if fsys.opens[app] != 1 {
		t.Errorf("jar re-read despite cache: %d opens", fsys.opens[app])
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(app, future, future); err != nil {
		t.Fatal(err)
	}
	r.Resolve(context.Background(), cp, root, []string{"com.example.A"})
	if fsys.opens[app] != 2 {
		t.Errorf("changed jar not re-indexed: %d opens", fsys.opens[app])
	}
}

func TestResolveExecutableJarAndManifest(t *testing.T) {
	root := t.TempDir()
	extra := filepath.Join(root, "lib", "extra lib.jar")
	writeJar(t, extra, "", "org/extra/E.class")
	// Class-Path is wrapped onto a 72-byte continuation line.
	manifest := "Manifest-Version: 1.0\r\nMain-Class: com.example.Boot\r\nClass-Path: lib/extra%20lib.jar http://example.com/r\r\n emote.jar\r\n\r\nName: x\r\nClass-Path: ignored.jar\r\n"
	boot := filepath.Join(root, "boot.jar")
	writeJar(t, boot, manifest, "BOOT-INF/classes/com/example/Boot.class", "org/springframework/boot/loader/Launcher.class")

	r, _ := newTestResolver()
	got := r.Resolve(context.Background(), LaunchClassPath{Jar: "boot.jar", Explicit: true}, root,
		[]string{"com.example.Boot", "org.springframework.boot.loader.Launcher", "org.extra.E", "com.example.Nope"})
	want := map[string]CodeSource{
		"com.example.Boot":                         {Jar: boot, JarEntry: "BOOT-INF/classes/com/example/Boot.class"},
		"org.springframework.boot.loader.Launcher": {Jar: boot, JarEntry: "org/springframework/boot/loader/Launcher.class"},
		"org.extra.E":                              {Jar: extra, JarEntry: "org/extra/E.class"},
	}
	if len(got) != len(want) {
		t.Errorf("got %+v", got)
	}
	for class, w := range want {
		if got[class] != w {
			t.Errorf("%s = %+v, want %+v", class, got[class], w)
		}
	}
}

func TestResolveBounds(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app.jar")
	writeJar(t, app, "", "a/A.class", "a/B.class", "a/C.class")
	cp := LaunchClassPath{Explicit: true, ClassPath: []string{app}}

	r, _ := newTestResolver()
	r.Limits.MaxJarEntries = 2
	if got := r.Resolve(context.Background(), cp, "", []string{"a.A"}); got != nil {
		t.Errorf("jar over the entry bound was indexed: %+v", got)
	}

	r, _ = newTestResolver()
	r.Limits.MaxClasses = 1
	if got := r.Resolve(context.Background(), cp, "", []string{"a.A", "a.B"}); len(got) != 1 {
		t.Errorf("class bound ignored: %+v", got)
	}

	// The clock jumps past the budget after the first lookup.
	r, _ = newTestResolver()
	calls := 0
	start := time.Unix(1000, 0)
	r.Now = func() time.Time {
		calls++
		if calls > 2 {
			return start.Add(time.Hour)
		}
		return start
	}
	if got := r.Resolve(context.Background(), cp, "", []string{"a.A", "a.B", "a.C"}); len(got) != 1 {
		t.Errorf("time budget ignored: %+v", got)
	}

	// Relative entries without a cwd are skipped, not resolved against ours.
	r, _ = newTestResolver()
	if got := r.Resolve(context.Background(), LaunchClassPath{Explicit: true, ClassPath: []string{"app.jar"}}, "", []string{"a.A"}); got != nil {
		t.Errorf("relative entry resolved without cwd: %+v", got)
	}
	if got := r.Resolve(context.Background(), cp, "", nil); got != nil {
		t.Errorf("no classes: %+v", got)
	}
}

func TestCodeSourcesForDump(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app.jar")
	writeJar(t, app, "", "com/example/A.class")
	dump := ParseThreadDump(`"main" #1 prio=5 os_prio=31 tid=0x1 nid=0x103 runnable
   java.lang.Thread.State: RUNNABLE
	at com.example.A.run(A.java:10)
	at com.example.A$$Lambda/0x000000080100a000.run(Unknown Source)
	at java.base@21/java.lang.Thread.run(Thread.java:1583)
	- locked <0x1> (a com.example.A)
`, time.Unix(0, 0))
	r, _ := newTestResolver()
	got := r.CodeSourcesForDump(context.Background(), dump, LaunchClassPath{Explicit: true, ClassPath: []string{app}}, "")
	if len(got) != 1 || got["com.example.A"].Jar != app {
		t.Errorf("got %+v", got)
	}
}
