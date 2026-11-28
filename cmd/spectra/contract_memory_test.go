package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/contracttest"
	"github.com/kaeawc/spectra/internal/fsutil"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/memstate"
)

// contractNow is the collection time the memory cases inject.
const contractNow = "2026-11-23T10:00:00.123456789Z"

// caseRoot writes (or checks) files under the case's root/ tree, the
// filesystem the Rust replay serves at absolute paths, and returns its path.
func caseRoot(t *testing.T, c contracttest.Case, files map[string]string) string {
	t.Helper()
	root := filepath.Join(c.Dir, "root")
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if *contracttest.Update {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := fsutil.WriteFileAtomic(path, []byte(files[name]), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path) // #nosec G304 -- path is under contract/cases
		if err != nil {
			t.Fatalf("%s: %v (write it with -contract-update)", path, err)
		}
		if string(got) != files[name] {
			t.Fatalf("%s no longer matches the test fixture; rewrite it with -contract-update", path)
		}
	}
	return root
}

func uint32p(v uint32) *uint32 { return &v }

// darwinSamples are raw macOS readings: memorystatus_level in range (it is
// pressure_free_percent, PR #755), and out of range with no pressure level
// (the page fraction is the fallback).
var darwinSamples = map[string]memstate.DarwinSample{
	"darwin": {
		PageSizeBytes: 16384,
		PhysicalBytes: 17179869184,
		Swap:          memstate.SwapUsage{TotalBytes: 2147483648, UsedBytes: 1073741824, FreeBytes: 1073741824, Encrypted: true},
		VM: memstate.DarwinVMCounts{
			Wire: 150000, Active: 300000, Inactive: 290000, Speculative: 5000, Free: 20000, Purgeable: 3000,
			External: 250000, Internal: 330000, Compressor: 60000, UncompressedCompressor: 185000,
			Compressions: 123456789, Decompressions: 98765432, Swapins: 1024, Swapouts: 2048,
		},
		PressureLevel:     uint32p(1),
		MemorystatusLevel: uint32p(42),
	},
	"darwin-fallback": {
		PageSizeBytes: 4096,
		PhysicalBytes: 8589934592,
		VM: memstate.DarwinVMCounts{
			Wire: 400000, Active: 700000, Inactive: 650000, Speculative: 4321, Free: 123457, Purgeable: 10,
			External: 500000, Internal: 900000, Compressions: 7, Decompressions: 3,
		},
		MemorystatusLevel: uint32p(255),
	},
}

const linuxMeminfo = `MemTotal:        4000000 kB
MemFree:         1000000 kB
MemAvailable:    3000000 kB
Buffers:           50000 kB
Cached:          2000000 kB
Active:          1100000 kB
Inactive:        1500000 kB
Active(file):    1000000 kB
Inactive(file):  1000000 kB
Unevictable:           4 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
Zswap:            100000 kB
Zswapped:         300000 kB
AnonPages:        400000 kB
HugePages_Total:       0
`

// linuxMeminfoOld has no MemAvailable (kernels before 3.14) and little free
// memory, so available is free+cached+buffers and pressure is Warning.
const linuxMeminfoOld = `MemTotal:        2000000 kB
MemFree:           50000 kB
Buffers:           20000 kB
Cached:           130000 kB
Active:          1500000 kB
Inactive:         300000 kB
Active(file):      90000 kB
Inactive(file):    40000 kB
AnonPages:       1600000 kB
SwapTotal:             0 kB
SwapFree:              0 kB
`

func psiFile(some, full string) string {
	out := "some " + some + "\n"
	if full != "" {
		out += "full " + full + "\n"
	}
	return out
}

// linuxMemoryTrees are /proc trees for `spectra memory` on Linux.
var linuxMemoryTrees = map[string]struct {
	pageSize uint64
	files    map[string]string
}{
	"linux": {4096, map[string]string{
		"proc/meminfo":         linuxMeminfo,
		"proc/vmstat":          "nr_free_pages 1\npswpin 12\npswpout 34\n",
		"proc/pressure/cpu":    psiFile("avg10=0.50 avg60=0.40 avg300=0.30 total=1000", "avg10=0.00 avg60=0.00 avg300=0.00 total=0"),
		"proc/pressure/io":     psiFile("avg10=0.12 avg60=0.30 avg300=0.15 total=793942", "avg10=0.10 avg60=0.18 avg300=0.08 total=496093"),
		"proc/pressure/memory": psiFile("avg10=12.50 avg60=3.00 avg300=1.00 total=100", "avg10=2.25 avg60=0.50 avg300=0.10 total=50"),
	}},
	"linux-nopsi": {65536, map[string]string{
		"proc/meminfo": linuxMeminfoOld,
	}},
}

// TestContractMemoryCases records `spectra memory` (JSON and text) on macOS
// from raw readings and on Linux from /proc trees, plus its flag errors.
// Replayed by crates/cli/tests/contract.rs.
func TestContractMemoryCases(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "")
	at, err := time.Parse(time.RFC3339Nano, contractNow)
	if err != nil {
		t.Fatal(err)
	}
	orig := collectMemory
	defer func() { collectMemory = orig }()
	record := func(name string, input map[string]any, args []string) {
		c := contracttest.New(t, "memory", name)
		input["args"] = args
		c.Input(t, input)
		var code int
		stdout, stderr := captureStdio(t, func() { code = dispatch(args) })
		c.Expect(t, stdout, code)
		expectStderr(t, c, stderr)
	}
	for _, mode := range []string{"json", "text"} {
		args := []string{"memory"}
		if mode == "json" {
			args = append(args, "--json")
		}
		for name, sample := range darwinSamples {
			collectMemory = func() (memstate.MemoryState, error) { return memstate.FromDarwinSample(sample, at), nil }
			record(mode+"-"+name, map[string]any{"os": "darwin", "now": contractNow, "darwin_sample": sample}, args)
		}
		for name, tree := range linuxMemoryTrees {
			c := contracttest.New(t, "memory", mode+"-"+name)
			root := caseRoot(t, c, tree.files)
			collectMemory = func() (memstate.MemoryState, error) {
				return memstate.CollectLinux(memstate.Options{ProcRoot: filepath.Join(root, "proc"), PageSize: tree.pageSize, Now: func() time.Time { return at }})
			}
			record(mode+"-"+name, map[string]any{"os": "linux", "now": contractNow, "page_size": tree.pageSize}, args)
		}
	}

	// A meminfo without MemTotal fails with a message that names no path.
	c := contracttest.New(t, "memory", "linux-no-memtotal")
	root := caseRoot(t, c, map[string]string{"proc/meminfo": "HugePages_Total: 0\n"})
	collectMemory = func() (memstate.MemoryState, error) {
		return memstate.CollectLinux(memstate.Options{ProcRoot: filepath.Join(root, "proc"), PageSize: 4096, Now: func() time.Time { return at }})
	}
	record("linux-no-memtotal", map[string]any{"os": "linux", "now": contractNow, "page_size": 4096}, []string{"memory"})

	collectMemory = func() (memstate.MemoryState, error) {
		t.Fatal("a flag error must not collect")
		return memstate.MemoryState{}, nil
	}
	for name, args := range map[string][]string{
		"flag-watch-zero":    {"memory", "--watch", "0"},
		"flag-watch-invalid": {"memory", "--watch=5x"},
		"flag-unknown":       {"memory", "--nope"},
		"flag-help":          {"memory", "-h"},
	} {
		record(name, map[string]any{"os": "darwin"}, args)
	}
}

func pressureCgroupTree() map[string]string {
	return map[string]string{
		"proc/pressure/cpu":    psiFile("avg10=4.76 avg60=26.31 avg300=10.34 total=36510539", "avg10=0.00 avg60=0.00 avg300=0.00 total=0"),
		"proc/pressure/memory": psiFile("avg10=12.50 avg60=3.00 avg300=1.00 total=100", "avg10=2.25 avg60=0.50 avg300=0.10 total=50"),
		"proc/pressure/io":     psiFile("avg10=0.12 avg60=0.30 avg300=0.15 total=793942", "avg10=0.10 avg60=0.18 avg300=0.08 total=496093"),
		"proc/42/cgroup":       "0::/svc.slice/app.service\n",

		"sys/fs/cgroup/cgroup.controllers":                        "cpu memory pids\n",
		"sys/fs/cgroup/svc.slice/memory.max":                      "1073741824\n",
		"sys/fs/cgroup/svc.slice/app.service/cgroup.controllers":  "cpu memory pids\n",
		"sys/fs/cgroup/svc.slice/app.service/memory.max":          "max\n",
		"sys/fs/cgroup/svc.slice/app.service/memory.high":         "805306368\n",
		"sys/fs/cgroup/svc.slice/app.service/memory.current":      "1048576000\n",
		"sys/fs/cgroup/svc.slice/app.service/memory.swap.current": "4194304\n",
		"sys/fs/cgroup/svc.slice/app.service/memory.stat":         "anon 1000000000\nfile 48576000\nkernel 1\n",
		"sys/fs/cgroup/svc.slice/app.service/memory.events":       "low 0\nhigh 5\nmax 12\noom 3\noom_kill 2\n",
		"sys/fs/cgroup/svc.slice/app.service/memory.pressure":     psiFile("avg10=31.00 avg60=9.00 avg300=2.00 total=7", "avg10=11.00 avg60=1.00 avg300=0.50 total=3"),
		"sys/fs/cgroup/svc.slice/app.service/cpu.stat":            "usage_usec 5000000\nuser_usec 4000000\nsystem_usec 1000000\nnr_periods 100\nnr_throttled 40\nthrottled_usec 2500000\n",
		"sys/fs/cgroup/svc.slice/app.service/cpu.max":             "50000 100000\n",
		"sys/fs/cgroup/svc.slice/app.service/cpu.pressure":        psiFile("avg10=10.00 avg60=2.00 avg300=1.00 total=9", "avg10=5.00 avg60=1.00 avg300=1.00 total=1"),
		"sys/fs/cgroup/svc.slice/app.service/io.pressure":         psiFile("avg10=0.00 avg60=0.00 avg300=0.00 total=0", "avg10=1.50 avg60=0.00 avg300=0.00 total=1"),
		"sys/fs/cgroup/svc.slice/app.service/pids.current":        "95\n",
		"sys/fs/cgroup/svc.slice/app.service/pids.max":            "100\n",
	}
}

// pressureCases are `spectra pressure` runs on Linux fixture trees.
var pressureCases = map[string]struct {
	files map[string]string
	args  []string
}{
	"linux-pid": {pressureCgroupTree(), []string{"--pid", "42"}},
	"linux-healthy": {map[string]string{
		"proc/pressure/cpu":    psiFile("avg10=0.00 avg60=0.00 avg300=0.00 total=0", ""),
		"proc/pressure/memory": psiFile("avg10=0.00 avg60=0.00 avg300=0.00 total=0", "avg10=0.00 avg60=0.00 avg300=0.00 total=0"),
		"proc/pressure/io":     psiFile("avg10=9.99 avg60=0.00 avg300=0.00 total=0", "avg10=0.99 avg60=0.00 avg300=0.00 total=0"),
	}, nil},
	// No PSI, and pid 7 is on cgroup v1 only: two notes and no findings.
	"linux-nopsi": {map[string]string{"proc/7/cgroup": "4:memory:/a\n3:cpu:/b\n"}, []string{"--pid=7"}},
}

// TestContractPressureCases records `spectra pressure` (JSON and text) on
// Linux fixture trees, its parse and flag errors, and the macOS refusal.
// Replayed by crates/cli/tests/contract.rs.
func TestContractPressureCases(t *testing.T) {
	record := func(name, goos string, args []string, procRoot, cgroupRoot string) {
		restore := hostos.SetForTest(map[string]hostos.Kind{"darwin": hostos.Darwin, "linux": hostos.Linux}[goos])
		defer restore()
		c := contracttest.New(t, "pressure", name)
		c.Input(t, map[string]any{"args": append([]string{"pressure"}, args...), "os": goos})
		var stdout, stderr bytes.Buffer
		code := runPressureWithIO(args, &stdout, &stderr, procRoot, cgroupRoot)
		c.Expect(t, stdout.Bytes(), code)
		expectStderr(t, c, stderr.Bytes())
	}
	for _, mode := range []string{"json", "text"} {
		for name, pc := range pressureCases {
			c := contracttest.New(t, "pressure", mode+"-"+name)
			root := caseRoot(t, c, pc.files)
			args := pc.args
			if mode == "json" {
				args = append([]string{"--json"}, args...)
			}
			record(mode+"-"+name, "linux", args, filepath.Join(root, "proc"), filepath.Join(root, "sys", "fs", "cgroup"))
		}
		args := []string{}
		if mode == "json" {
			args = []string{"--json"}
		}
		record(mode+"-darwin", "darwin", args, "/nonexistent", "/nonexistent")
	}

	c := contracttest.New(t, "pressure", "linux-bad-psi")
	root := caseRoot(t, c, map[string]string{
		"proc/pressure/cpu":    "some avg10=abc avg60=0.00 avg300=0.00 total=0\n",
		"proc/pressure/memory": psiFile("avg10=0.00 avg60=0.00 avg300=0.00 total=0", ""),
		"proc/pressure/io":     psiFile("avg10=0.00 avg60=0.00 avg300=0.00 total=0", ""),
	})
	record("linux-bad-psi", "linux", nil, filepath.Join(root, "proc"), filepath.Join(root, "sys", "fs", "cgroup"))
	record("flag-invalid-pid", "linux", []string{"--pid", "x"}, "/nonexistent", "/nonexistent")
}
