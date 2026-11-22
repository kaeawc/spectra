package detect

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// benchBundle builds on makeBundle, then overwrites Info.plist with a multi-key
// variant (identity, version, and several NS*UsageDescription privacy strings)
// and adds a nested Frameworks/Resources tree, so the benchmark exercises the
// plist-parse cache and the bundle walks the way a real inspection does.
func benchBundle(b *testing.B) string {
	b.Helper()
	app := makeBundle(b, "BenchApp")
	for _, sub := range []string{
		"Contents/Resources/app/lib",
		"Contents/Frameworks/Some.framework/Resources",
	} {
		if err := os.MkdirAll(filepath.Join(app, sub), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>main</string>
<key>CFBundleIdentifier</key><string>com.example.bench</string>
<key>CFBundleShortVersionString</key><string>1.2.3</string>
<key>CFBundleVersion</key><string>456</string>
<key>SUFeedURL</key><string>https://example.com/appcast.xml</string>
<key>NSCameraUsageDescription</key><string>needs camera</string>
<key>NSMicrophoneUsageDescription</key><string>needs mic</string>
<key>NSLocationUsageDescription</key><string>needs location</string>
</dict></plist>`
	writes := map[string][]byte{
		"Contents/Info.plist":         []byte(plist),
		"Contents/MacOS/main":         []byte("\x7fELF placeholder binary with some strings"),
		"Contents/Resources/app/x.js": []byte("console.log('hi')"),
	}
	for rel, data := range writes {
		if err := os.WriteFile(filepath.Join(app, rel), data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return app
}

// BenchmarkDetectBundle measures Detect over a bundle whose main executable is
// large and carries Rust panic-site markers, plus a wide Resources tree. It
// exercises the binary marker scan (read once, streamed with a reused buffer)
// and the bundle tree walk.
func BenchmarkDetectBundle(b *testing.B) {
	app := benchBundle(b)
	exe := make([]byte, 48<<20)
	marker := []byte("core::panicking rustc/ panicked at ")
	for off := 0; off+len(marker) < len(exe); off += 1 << 18 {
		copy(exe[off:], marker)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "main"), exe, 0o755); err != nil {
		b.Fatal(err)
	}
	res := filepath.Join(app, "Contents", "Resources", "wide")
	if err := os.MkdirAll(res, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		p := filepath.Join(res, "f"+strconv.Itoa(i)+".dat")
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DetectWith(app, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDetectWith measures the full single-app inspection pipeline. It is
// the guardrail for the plist-parse caching and single-walk work: a regression
// that reintroduces per-key plutil forks or duplicate bundle walks shows up
// here as extra time and allocations.
func BenchmarkDetectWith(b *testing.B) {
	app := benchBundle(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DetectWith(app, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
