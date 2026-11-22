package storagestate

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildBenchTree writes a synthetic tree of 20k small files spread over 200
// directories, two levels deep.
func buildBenchTree(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	for top := range 10 {
		for sub := range 20 {
			dir := filepath.Join(root, fmt.Sprintf("top-%02d", top), fmt.Sprintf("sub-%02d", sub))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatal(err)
			}
			for f := range 100 {
				p := filepath.Join(dir, fmt.Sprintf("file-%03d.log", f))
				if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	return root
}

func BenchmarkWalkTree(b *testing.B) {
	root := buildBenchTree(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if u := walkTree(root, filepath.Join(root, "top-00")); u.Total == 0 {
			b.Fatal("empty walk")
		}
	}
}

func BenchmarkLogWalker(b *testing.B) {
	root := buildBenchTree(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		w := newLogWalker(maxLogWalkWorkers)
		w.root(root, "")
		if len(w.wait()) == 0 {
			b.Fatal("no logs")
		}
	}
}
