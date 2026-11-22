package detect

import (
	"bytes"
	"io"
	"sync"
)

var (
	rustNeedles = [][]byte{
		[]byte("core::panicking"),
		[]byte("rust_panic"),
		[]byte("rustc/"),
		[]byte("panicked at "),
		[]byte("RUST_BACKTRACE"),
	}
	goNeedle    = []byte("\xff Go buildinf:") // prefix of the __go_buildinfo magic
	wailsNeedle = []byte("github.com/wailsapp/wails")
)

// maxMarkerLen is the longest needle; a chunk boundary carries maxMarkerLen-1
// bytes forward so a needle straddling it is still found.
var maxMarkerLen = func() int {
	n := max(len(goNeedle), len(wailsNeedle))
	for _, nd := range rustNeedles {
		n = max(n, len(nd))
	}
	return n
}()

const markerChunk = 1 << 20

// markerBufPool recycles the scan buffer across files so a bundle with many
// binaries does not allocate one 1 MiB buffer each.
var markerBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, markerChunk+maxMarkerLen)
		return &b
	},
}

// countNew counts non-overlapping occurrences of needle in region that end in
// the new bytes, i.e. past the first carried bytes. Matches lying wholly in
// the carried tail were already counted in the previous chunk.
func countNew(region []byte, carried int, needle []byte) int {
	start := max(carried-len(needle)+1, 0)
	if start >= len(region) {
		return 0
	}
	return bytes.Count(region[start:], needle)
}

// scanMarkersReader streams r once through a single reused buffer and
// collects every marker in that pass. Counts are exact: equal to scanning the
// whole content in memory.
func scanMarkersReader(r io.Reader) (m binaryMarkers) {
	bp := markerBufPool.Get().(*[]byte)
	defer markerBufPool.Put(bp)
	buf := *bp
	carried := 0
	for {
		n, err := r.Read(buf[carried : carried+markerChunk])
		if n > 0 {
			region := buf[:carried+n]
			for _, needle := range rustNeedles {
				m.rustHits += countNew(region, carried, needle)
			}
			if !m.hasGoBuildID && countNew(region, carried, goNeedle) > 0 {
				m.hasGoBuildID = true
			}
			if !m.isWails && countNew(region, carried, wailsNeedle) > 0 {
				m.isWails = true
			}
			carried = min(maxMarkerLen-1, len(region))
			copy(buf, region[len(region)-carried:])
		}
		if err != nil {
			return m
		}
	}
}

// markerCache memoizes marker scans for the duration of one Detect call so a
// binary the classifier and the Rust inspector both need is read once.
type markerCache struct {
	scan func(path string) binaryMarkers

	mu   sync.Mutex
	done map[string]binaryMarkers
}

func newMarkerCache(scan func(path string) binaryMarkers) *markerCache {
	return &markerCache{scan: scan, done: map[string]binaryMarkers{}}
}

func (c *markerCache) ScanMarkers(path string) binaryMarkers {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.done[path]; ok {
		return m
	}
	m := c.scan(path)
	c.done[path] = m
	return m
}

func (c *markerCache) scanMarkers(path string) binaryMarkers { return c.ScanMarkers(path) }
