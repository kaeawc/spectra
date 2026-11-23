package memstate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/pressure"
)

const meminfoFixture = `MemTotal:        4000000 kB
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

func writeProc(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func psiFixture(someAvg10, fullAvg10 string) string {
	return "some avg10=" + someAvg10 + " avg60=0.00 avg300=0.00 total=1\nfull avg10=" + fullAvg10 + " avg60=0.00 avg300=0.00 total=1\n"
}

func TestParseMeminfo(t *testing.T) {
	ms, err := ParseMeminfo([]byte(meminfoFixture))
	if err != nil {
		t.Fatal(err)
	}
	const k = 1024
	checks := []struct {
		name      string
		got, want uint64
	}{
		{"physical", ms.PhysicalBytes, 4000000 * k},
		{"free", ms.Free, 1000000 * k},
		{"available", ms.AvailableBytes, 3000000 * k},
		{"file", ms.FileBacked, 2000000 * k},
		{"anon", ms.Anonymous, 400000 * k},
		{"swap total", ms.Swap.TotalBytes, 2000000 * k},
		{"swap used", ms.Swap.UsedBytes, 500000 * k},
		{"zswap", ms.CompressorOccupied, 100000 * k},
		{"zswapped", ms.CompressorStored, 300000 * k},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if ms.CompressorRatio != 3 {
		t.Errorf("ratio = %v, want 3", ms.CompressorRatio)
	}
	if ms.PressureFreePercent != 75 {
		t.Errorf("free percent = %v, want 75", ms.PressureFreePercent)
	}
	if ms.OS != "linux" {
		t.Errorf("os = %q", ms.OS)
	}
}

func TestParseMeminfoWithoutMemAvailable(t *testing.T) {
	ms, err := ParseMeminfo([]byte("MemTotal: 1000 kB\nMemFree: 100 kB\nCached: 200 kB\nBuffers: 50 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ms.AvailableBytes != 350*1024 {
		t.Errorf("available = %d, want free+cached+buffers", ms.AvailableBytes)
	}
}

func TestParseMeminfoRejectsGarbage(t *testing.T) {
	if _, err := ParseMeminfo([]byte("nonsense\n")); err == nil {
		t.Fatal("want an error without MemTotal")
	}
}

func TestCollectLinux(t *testing.T) {
	root := writeProc(t, map[string]string{
		"meminfo":         meminfoFixture,
		"vmstat":          "nr_free_pages 1\npswpin 12\npswpout 34\n",
		"pressure/cpu":    psiFixture("0.00", "0.00"),
		"pressure/io":     psiFixture("0.00", "0.00"),
		"pressure/memory": psiFixture("2.50", "0.00"),
	})
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	ms, err := collectLinux(Options{ProcRoot: root, PageSize: 4096, Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	if ms.SwapIns != 12 || ms.SwapOuts != 34 {
		t.Errorf("swap ins/outs = %d/%d", ms.SwapIns, ms.SwapOuts)
	}
	if ms.PSI == nil || ms.PSI.Some.Avg10 != 2.5 {
		t.Errorf("psi = %+v", ms.PSI)
	}
	if ms.PressureLevel != PressureNormal || ms.PressureReason == "" {
		t.Errorf("pressure = %s (%q), want Normal with a reason", ms.PressureLevel, ms.PressureReason)
	}
	if !ms.CollectedAt.Equal(at) || ms.PageSizeBytes != 4096 {
		t.Errorf("collected_at=%v page=%d", ms.CollectedAt, ms.PageSizeBytes)
	}
}

func TestCollectLinuxWithoutPSIOrVMStat(t *testing.T) {
	ms, err := collectLinux(Options{ProcRoot: writeProc(t, map[string]string{"meminfo": meminfoFixture})})
	if err != nil {
		t.Fatal(err)
	}
	if ms.PSI != nil {
		t.Errorf("psi = %+v, want nil when the kernel has none", ms.PSI)
	}
	if ms.PressureLevel != PressureNormal {
		t.Errorf("pressure = %s", ms.PressureLevel)
	}
}

func TestCollectLinuxMissingMeminfo(t *testing.T) {
	if _, err := collectLinux(Options{ProcRoot: t.TempDir()}); err == nil {
		t.Fatal("want an error without meminfo")
	}
}

func TestLinuxPressure(t *testing.T) {
	line := func(some, full float64) MemoryState {
		ms := MemoryState{PressureFreePercent: 50}
		ms.PSI = psiResource(some, full)
		return ms
	}
	cases := []struct {
		name string
		ms   MemoryState
		want PressureLevel
	}{
		{"plenty available, no stalls", line(0, 0), PressureNormal},
		{"some stall", line(12, 0), PressureWarning},
		{"brief full stall", line(5, 2), PressureWarning},
		{"sustained full stall", line(40, 15), PressureCritical},
		{"low available, no PSI", MemoryState{PressureFreePercent: 10}, PressureWarning},
		{"almost none available", MemoryState{PressureFreePercent: 3}, PressureCritical},
	}
	for _, c := range cases {
		if got, _ := linuxPressure(c.ms); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func psiResource(some, full float64) *pressure.Resource {
	return &pressure.Resource{Some: pressure.Line{Avg10: some}, Full: &pressure.Line{Avg10: full}}
}

func TestCollectDispatchesOnHostOS(t *testing.T) {
	if hostos.Current() == hostos.Darwin {
		t.Skip("darwin uses the cgo collector")
	}
	defer hostos.SetForTest(hostos.Unknown)()
	if _, err := CollectWithOptions(Options{}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("unknown OS: err = %v, want ErrNotSupported", err)
	}
}
