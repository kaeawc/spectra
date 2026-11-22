package detect

import (
	"bytes"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// countingPythonFS wraps the real FS and counts per-path reads and walks.
type countingPythonFS struct {
	osPythonAppFS
	mu    sync.Mutex
	reads map[string]int
	walks []string
}

func (c *countingPythonFS) note(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads[name]++
}

func (c *countingPythonFS) ReadFile(name string) ([]byte, error) {
	c.note(name)
	return c.osPythonAppFS.ReadFile(name)
}

func (c *countingPythonFS) Open(name string) (pythonAppFile, error) {
	c.note(name)
	return c.osPythonAppFS.Open(name)
}

func (c *countingPythonFS) WalkDir(root string, fn fs.WalkDirFunc) error {
	c.walks = append(c.walks, root)
	return c.osPythonAppFS.WalkDir(root, fn)
}

// referenceMarkers is what the pre-streaming passes computed from a whole-file
// read.
func referenceMarkers(data []byte) pythonFileMarkers {
	m := pythonFileMarkers{
		launcherPython: containsAnyNeedle(data, pythonLauncherNeedles),
		envSensitive:   containsAnyNeedle(data, pythonEnvNeedles),
		nuitka:         bytes.Contains(bytes.ToLower(data), nuitkaNeedle),
	}
	if sm := externalPythonInterpreterRE.FindSubmatch(data); len(sm) > 1 {
		m.interpreter = string(sm[1])
	}
	return m
}

func TestPythonMarkerSearchMatchesWholeFileRegex(t *testing.T) {
	frags := []string{
		"/usr/", "/opt/homebrew/", "/Users/", "/Users/me/", "python", "Python.framework", "lib",
		"3", ".", "1", " ", "\"", "'", "\n", "x", "/", "\x00", "local/", "bin/", "PYTHONPATH=",
		"DYLD_INSERT_LIBRARIES=", "nuit", "NUI", "ka", "\u0130", "\u212a", "tk", "\xe2", "\xc4",
	}
	rng := rand.New(rand.NewSource(1))
	matches := 0
	for iter := 0; iter < 8000; iter++ {
		var b strings.Builder
		for n := rng.Intn(60); n > 0; n-- {
			b.WriteString(frags[rng.Intn(len(frags))])
		}
		data := []byte(b.String())
		want := referenceMarkers(data)
		chunk := 1 + rng.Intn(48)
		tokenCap := 1 + rng.Intn(40)
		ms := newPythonMarkerSearch(true, true, chunk, tokenCap, bytes.NewReader(data))
		got, err := ms.run(bytes.NewReader(data), make([]byte, ms.bufLen()))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("chunk=%d tokenCap=%d data=%q\n got %+v\nwant %+v", chunk, tokenCap, data, got, want)
		}
		if want.interpreter != "" {
			matches++
		}
	}
	if matches < 500 {
		t.Fatalf("only %d inputs matched the interpreter regex; generator too weak", matches)
	}
}

func TestPythonMarkerSearchFindsMarkersStraddlingChunkBoundary(t *testing.T) {
	launcher := make([]byte, pythonScanChunk-5)
	launcher = append(launcher, "PYTHONPATH=x /opt/homebrew/bin/python3.12\n"...)
	so := make([]byte, pythonScanChunk-3)
	so = append(so, "NuItKa"...)
	for name, tc := range map[string]struct {
		data         []byte
		launcher, so bool
		want         pythonFileMarkers
	}{
		"launcher": {launcher, true, false, pythonFileMarkers{launcherPython: true, envSensitive: true, interpreter: "/opt/homebrew/bin/python3.12"}},
		"so":       {so, false, true, pythonFileMarkers{nuitka: true}},
	} {
		t.Run(name, func(t *testing.T) {
			ms := newPythonMarkerSearch(tc.launcher, tc.so, 0, 0, bytes.NewReader(tc.data))
			got, err := ms.run(bytes.NewReader(tc.data), make([]byte, ms.bufLen()))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPythonScanOpensEachFileAtMostOnce(t *testing.T) {
	app := makeBundle(t, "FakeOnce")
	writeFile(t, filepath.Join(app, "Contents/MacOS/main"), "\x00\x00/usr/lib/libSystem.B.dylib\x00 nothing to see\n")
	writeFile(t, filepath.Join(app, "Contents/MacOS/run.sh"), "#!/bin/sh\nexec /usr/local/bin/python3.12 \"$@\"\n")
	writeFile(t, filepath.Join(app, "Contents/MacOS/empty"), "")
	writeFile(t, filepath.Join(app, "Contents/Resources/lib/a.so"), "plain native code")
	writeFile(t, filepath.Join(app, "Contents/Resources/lib/b.so"), "more native code")
	writeFile(t, filepath.Join(app, "Contents/Resources/venv/pyvenv.cfg"), "version = 3.12.1\n")
	writeFile(t, filepath.Join(app, "Contents/Resources/lib/x-1.0.dist-info/METADATA"), "Name: x\nVersion: 1.0\n")

	cfs := &countingPythonFS{reads: map[string]int{}}
	got := pythonAppScanner{fs: cfs, commands: &fakePythonCommandRunner{}}.Scan(app)
	if got == nil || got.Packaging != "custom" || got.RuntimePath != "/usr/local/bin/python3.12" {
		t.Fatalf("unexpected result %#v", got)
	}
	if !hasHint(got.RiskHints, "external interpreter path") {
		t.Fatalf("RiskHints = %v", got.RiskHints)
	}
	for path, n := range cfs.reads {
		if n > 1 {
			t.Errorf("%s read %d times, want at most once", path, n)
		}
	}
	if _, ok := cfs.reads[filepath.Join(app, "Contents/MacOS/empty")]; ok {
		t.Error("empty file was opened")
	}
	if len(cfs.walks) != 1 {
		t.Errorf("WalkDir roots = %v, want a single Contents walk", cfs.walks)
	}
}

func TestPythonScanWithoutContentsDirFallsBackPerRoot(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Linked.app")
	real := filepath.Join(t.TempDir(), "RealContents")
	writeFile(t, filepath.Join(real, "MacOS/run.sh"), "#!/bin/sh\nPYTHONPATH=x exec /opt/homebrew/bin/python3 \"$@\"\n")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(app, "Contents")); err != nil {
		t.Fatal(err)
	}
	got := pythonAppScanner{fs: osPythonAppFS{}, commands: &fakePythonCommandRunner{}}.Scan(app)
	if got == nil || got.Packaging != "custom" || got.RuntimePath != "/opt/homebrew/bin/python3" || !hasHint(got.RiskHints, "environment-sensitive launcher") {
		t.Fatalf("unexpected result %#v", got)
	}
}
