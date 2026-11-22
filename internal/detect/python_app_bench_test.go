package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// makeSyntheticPythonBundle builds a "custom" Python app: a 50 MB mostly
// zero-filled (sparse) Mach-O-like main executable, a shell launcher that
// execs an external interpreter, and a native extension.
func makeSyntheticPythonBundle(tb testing.TB) string {
	tb.Helper()
	app := filepath.Join(tb.TempDir(), "Synthetic.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		tb.Fatal(err)
	}
	f, err := os.Create(filepath.Join(macos, "app"))
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("\xcf\xfa\xed\xfe\x0c\x00\x00\x00/usr/lib/libSystem.B.dylib\x00"), 0); err != nil {
		tb.Fatal(err)
	}
	if err := f.Truncate(50 << 20); err != nil {
		tb.Fatal(err)
	}
	if err := f.Close(); err != nil {
		tb.Fatal(err)
	}
	writeTB(tb, filepath.Join(macos, "run.sh"), "#!/bin/sh\ncd \"$(dirname \"$0\")\"\nexec /usr/bin/python3 ../Resources/main.py \"$@\"\n")
	writeTB(tb, filepath.Join(app, "Contents", "Resources", "main.py"), "print('hello')\n")
	ext := make([]byte, 1<<20)
	copy(ext[4096:], "PyInit__speedups")
	writeTB(tb, filepath.Join(app, "Contents", "Resources", "lib", "_speedups.so"), string(ext))
	return app
}

func writeTB(tb testing.TB, path, body string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkPythonScan(b *testing.B) {
	app := makeSyntheticPythonBundle(b)
	s := pythonAppScanner{fs: osPythonAppFS{}, commands: &fakePythonCommandRunner{}}
	got := s.Scan(app)
	if got == nil || got.Packaging != "custom" || got.RuntimePath != "/usr/bin/python3" {
		b.Fatalf("unexpected scan result: %#v", got)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Scan(app)
	}
}
