package spindump

import (
	"strings"
	"testing"
)

const sampleReport = `Date/Time:        2026-08-30 12:00:00 -0700
Duration:         2.01s
Steps:            201

Process:          Foo [4012]
Path:             /Applications/Foo.app/Contents/MacOS/Foo
Architecture:     arm64

  201  Thread_1001   DispatchQueue_1: com.apple.main-thread  (serial)
    201  start + 1903 (dyld + 21631) [0x1a2b3c4d]
      201  main + 128 (Foo + 4096) [0x104a01000]
        150  -[Renderer draw:] + 44 (Foo + 8192) [0x104a02000]
          150  CGContextFillRects + 12 (CoreGraphics + 100) [0x1b0000000]
        51  -[Renderer idle] + 8 (Foo + 9000) [0x104a03000]

Process:          bar [55]
Path:             /usr/bin/bar

  40  Thread_2002
    40  poll + 10 (libsystem_kernel.dylib + 20) [0x1c0000000]
`

func TestParseProcessesAndDuration(t *testing.T) {
	rep := Parse(sampleReport)
	if rep.Duration != "2.01s" {
		t.Errorf("duration = %q", rep.Duration)
	}
	if len(rep.Processes) != 2 {
		t.Fatalf("processes = %d, want 2", len(rep.Processes))
	}
	foo := rep.Processes[0]
	if foo.Name != "Foo" || foo.PID != 4012 {
		t.Errorf("foo header = %q [%d]", foo.Name, foo.PID)
	}
}

func TestParseHeaviestRankedAndThreadHeadersExcluded(t *testing.T) {
	rep := Parse(sampleReport)
	foo := rep.Processes[0]
	if len(foo.Heaviest) == 0 {
		t.Fatal("expected heaviest frames for Foo")
	}
	// The hottest symbol should be one of the 201-sample frames, never the
	// thread/dispatch-queue header.
	for _, f := range foo.Heaviest {
		if f.Symbol == "" || containsThreadMarker(f.Symbol) {
			t.Errorf("thread header leaked into hotspots: %q", f.Symbol)
		}
	}
	top := foo.Heaviest[0]
	if top.Samples != 201 {
		t.Errorf("top samples = %d, want 201 (%q)", top.Samples, top.Symbol)
	}
	// Symbols must be stripped of the (Image + off), [addr], and "+ off" noise.
	if !hasSymbol(foo.Heaviest, "-[Renderer draw:]", 150) {
		t.Errorf("expected a clean -[Renderer draw:] frame at 150 samples, got %+v", foo.Heaviest)
	}
}

func TestParseStripsControlBytes(t *testing.T) {
	// A report whose process name and a frame carry terminal escape bytes must
	// come out sanitized.
	report := "Process:          Ev\x1b[31mil [7]\n  9  Thread_1\n    9  bad\x07sym + 1 (X + 1) [0x1]\n"
	rep := Parse(report)
	if len(rep.Processes) != 1 {
		t.Fatalf("processes = %d", len(rep.Processes))
	}
	if strings.ContainsRune(rep.Processes[0].Name, 0x1b) {
		t.Errorf("process name still has an escape byte: %q", rep.Processes[0].Name)
	}
	for _, f := range rep.Processes[0].Heaviest {
		if strings.ContainsRune(f.Symbol, 0x07) {
			t.Errorf("symbol still has a control byte: %q", f.Symbol)
		}
	}
}

func TestParseEmptyReport(t *testing.T) {
	if rep := Parse(""); len(rep.Processes) != 0 {
		t.Errorf("empty report should have no processes, got %+v", rep.Processes)
	}
}

func containsThreadMarker(s string) bool {
	return isThreadHeader(s)
}

func hasSymbol(frames []Frame, sym string, samples int) bool {
	for _, f := range frames {
		if f.Symbol == sym && f.Samples == samples {
			return true
		}
	}
	return false
}

func TestFrameSymbolStripping(t *testing.T) {
	cases := map[string]string{
		"main + 128 (Foo + 4096) [0x104a01000]":         "main",
		"-[Renderer draw:] + 44 (Foo + 8192) [0x1]":     "-[Renderer draw:]",
		"poll + 10 (libsystem_kernel.dylib + 20) [0x1]": "poll",
		"symbol_without_qualifiers":                     "symbol_without_qualifiers",
	}
	for in, want := range cases {
		if got := frameSymbol(in); got != want {
			t.Errorf("frameSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}
