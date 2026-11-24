package spindump

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kaeawc/spectra/internal/binaryimages"
)

const imagesReport = `Date/Time:        2026-11-20 10:15:02.118 -0800
OS Version:       macOS 15.1 (Build 24B83)
Architecture:     arm64e
Duration:         2.01s
Steps:            201

Process:          Foo [4012]
UUID:             8A2D1D4C-5E6F-3A2B-9C0D-1E2F3A4B5C6D
Path:             /Applications/Foo.app/Contents/MacOS/Foo
Identifier:       com.example.foo
Version:          2.3 (230)
Architecture:     arm64
Footprint:        41.2 MB
Num samples:      201 (1-201)

  Thread 0x1a2b    DispatchQueue "com.apple.main-thread"(1)    201 samples (1-201)    priority 46 (base 46)
  201  start + 6076 (dyld + 27544) [0x1a2b06b98]
    201  main + 128 (Foo + 4224) [0x104a01080]
      150  -[Renderer draw:] + 44 (Foo + 8236) [0x104a0202c]
        150  CGContextFillRects + 12 (CoreGraphics + 100) [0x1b0000064]
      51  ??? [0x2c0001234]

  Binary Images:
           0x104a00000 -        0x104a0ffff  com.example.foo 2.3 (230)          <8A2D1D4C-5E6F-3A2B-9C0D-1E2F3A4B5C6D>  /Applications/Foo.app/Contents/MacOS/Foo
           0x1a2b00000 -                ???  dyld 1231.3 (1231.3)               <B1C2D3E4-F5A6-3B7C-8D9E-0F1A2B3C4D5E>  /usr/lib/dyld
           0x1b0000000 -        0x1b05fffff  com.apple.CoreGraphics 2.0 (1856)  <C2D3E4F5-A6B7-3C8D-9E0F-1A2B3C4D5E6F>  /System/Library/Frameworks/CoreGraphics.framework/Versions/A/CoreGraphics

Process:          bar [55]
Path:             /usr/bin/bar

  Thread 0x3c4d    40 samples (1-40)    priority 31 (base 31)
  40  poll + 10 (libsystem_kernel.dylib + 20) [0x1c0000014]

  Binary Images:
           0x1c0000000 -        0x1c003ffff  libsystem_kernel.dylib (11215.41.3) <D3E4F5A6-B7C8-3D9E-0F1A-2B3C4D5E6F70>  /usr/lib/system/libsystem_kernel.dylib
`

func heaviestBySymbol(p ProcessReport) map[string]Frame {
	out := map[string]Frame{}
	for _, f := range p.Heaviest {
		out[f.Symbol] = f
	}
	return out
}

func TestParseFrameImages(t *testing.T) {
	rep := Parse(imagesReport)
	if len(rep.Processes) != 2 {
		t.Fatalf("processes = %d", len(rep.Processes))
	}
	foo := heaviestBySymbol(rep.Processes[0])
	want := map[string]binaryimages.Location{
		"main":               {Image: "Foo", ImagePath: "/Applications/Foo.app/Contents/MacOS/Foo", ImageUUID: "8A2D1D4C-5E6F-3A2B-9C0D-1E2F3A4B5C6D", Arch: "arm64", ImageBase: "0x104a00000", ImageOffset: "0x1080", Address: "0x104a01080"},
		"start":              {Image: "dyld", ImagePath: "/usr/lib/dyld", ImageUUID: "B1C2D3E4-F5A6-3B7C-8D9E-0F1A2B3C4D5E", Arch: "arm64", ImageBase: "0x1a2b00000", ImageOffset: "0x6b98", Address: "0x1a2b06b98"},
		"CGContextFillRects": {Image: "CoreGraphics", ImagePath: "/System/Library/Frameworks/CoreGraphics.framework/Versions/A/CoreGraphics", ImageUUID: "C2D3E4F5-A6B7-3C8D-9E0F-1A2B3C4D5E6F", Arch: "arm64", ImageBase: "0x1b0000000", ImageOffset: "0x64", Address: "0x1b0000064"},
	}
	for sym, w := range want {
		if got := foo[sym].Location; got != w {
			t.Errorf("%s = %+v, want %+v", sym, got, w)
		}
	}
	// 0x2c0001234 lies past CoreGraphics' end, the last image.
	if f, ok := foo["???"]; !ok || !f.IsZero() {
		t.Errorf("unknown frame = %+v (present %v)", f, ok)
	}
	// The second process's images are its own, and it falls back to the
	// report-level architecture.
	poll := heaviestBySymbol(rep.Processes[1])["poll"]
	if poll.ImageOffset != "0x14" || poll.Arch != "arm64e" || poll.Image != "libsystem_kernel.dylib" {
		t.Errorf("poll = %+v", poll)
	}
}

func TestParseUnknownFramesOmitFields(t *testing.T) {
	b, err := json.Marshal(Frame{Symbol: "x", Samples: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"symbol":"x","samples":1}` {
		t.Errorf("frame without image = %s", b)
	}
}

// A report without Binary Images encodes exactly as before.
func TestParseLegacyJSONByteIdentical(t *testing.T) {
	want, err := os.ReadFile("testdata/legacy.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(Parse(sampleReport), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("JSON changed:\n%s", got)
	}
}
