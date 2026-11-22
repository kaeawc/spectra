package detect

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

func TestScanMarkersReaderChunkBoundaries(t *testing.T) {
	needles := [][]byte{[]byte("rustc/"), rustNeedles[0], goNeedle, wailsNeedle}
	for _, needle := range needles {
		// Place the needle so it straddles every split point near the chunk
		// edge, including entirely inside the carried tail.
		for split := 1; split < len(needle); split++ {
			data := make([]byte, 2*markerChunk)
			copy(data[markerChunk-split:], needle)
			got := scanMarkersReader(bytes.NewReader(data))
			switch {
			case bytes.Equal(needle, goNeedle):
				if !got.hasGoBuildID {
					t.Fatalf("go needle split %d not found", split)
				}
			case bytes.Equal(needle, wailsNeedle):
				if !got.isWails {
					t.Fatalf("wails needle split %d not found", split)
				}
			default:
				if got.rustHits != 1 {
					t.Fatalf("%q split %d: rustHits=%d want 1", needle, split, got.rustHits)
				}
			}
		}
	}
}

func TestScanMarkersReaderExactCounts(t *testing.T) {
	// Needles at every offset around chunk edges must each count exactly once,
	// including ones lying wholly inside the carried tail.
	data := make([]byte, 3*markerChunk)
	want := 0
	for _, base := range []int{markerChunk, 2 * markerChunk} {
		for off := -2 * maxMarkerLen; off < maxMarkerLen; off += 20 {
			copy(data[base+off:], "rustc/")
			want++
		}
	}
	for _, r := range []io.Reader{bytes.NewReader(data), iotest.HalfReader(bytes.NewReader(data))} {
		if got := scanMarkersReader(r).rustHits; got != want {
			t.Fatalf("rustHits=%d want %d", got, want)
		}
	}
}

func TestMarkerCacheScansOnce(t *testing.T) {
	calls := map[string]int{}
	c := newMarkerCache(func(p string) binaryMarkers {
		calls[p]++
		return binaryMarkers{rustHits: 7}
	})
	for range 3 {
		if c.ScanMarkers("/a").rustHits != 7 || c.scanMarkers("/a").rustHits != 7 {
			t.Fatal("wrong markers")
		}
	}
	c.ScanMarkers("/b")
	if calls["/a"] != 1 || calls["/b"] != 1 {
		t.Fatalf("scan calls = %v, want one per path", calls)
	}
}

func TestScanBinaryMarkersFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	body := bytes.Repeat([]byte("panicked at x RUST_BACKTRACE "), 60)
	if err := os.WriteFile(p, append([]byte("\xff Go buildinf:"), body...), 0o644); err != nil {
		t.Fatal(err)
	}
	m := scanBinaryMarkers(p)
	if !m.hasGoBuildID || m.isWails || m.rustHits != 120 {
		t.Fatalf("markers = %+v", m)
	}
}
