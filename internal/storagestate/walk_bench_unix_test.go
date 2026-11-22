//go:build unix

package storagestate

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"
)

// Real-tree benchmarks size worker counts. They read the directory named
// by SPECTRA_BENCH_ROOT (read-only; e.g. ~/Library) and are skipped without
// it. Besides wall time they report process user+sys CPU per walk, which is
// what concurrent readers inflate through kernel contention:
//
//	SPECTRA_BENCH_ROOT=$HOME/Library go test ./internal/storagestate/ \
//	  -run x -bench RealTree -benchtime 3x

func benchRealRoot(b *testing.B) string {
	b.Helper()
	root := os.Getenv("SPECTRA_BENCH_ROOT")
	if root == "" {
		b.Skip("SPECTRA_BENCH_ROOT not set")
	}
	return root
}

func cpuTime(b *testing.B) time.Duration {
	b.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		b.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func benchWorkers(b *testing.B, walk func(workers int)) {
	for _, workers := range []int{1, 2, 4, 8, 16} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			walk(workers) // warm the name cache so runs compare like for like
			start := cpuTime(b)
			b.ResetTimer()
			for range b.N {
				walk(workers)
			}
			b.StopTimer()
			b.ReportMetric((cpuTime(b)-start).Seconds()/float64(b.N), "cpu-s/op")
		})
	}
}

func BenchmarkWalkTreeRealTree(b *testing.B) {
	root := benchRealRoot(b)
	benchWorkers(b, func(workers int) { walkTreeWorkers(false, workers, root) })
}

func BenchmarkLogWalkerRealTree(b *testing.B) {
	root := benchRealRoot(b)
	benchWorkers(b, func(workers int) {
		w := newLogWalker(workers)
		w.root(root, "")
		w.wait()
	})
}
