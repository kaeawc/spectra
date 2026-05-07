package jvm

import (
	"testing"
)

func TestParseGCStats(t *testing.T) {
	// Sample output from `jstat -gc <pid>` on a G1 JVM.
	out := ` S0C    S1C    S0U    S1U      EC       EU        OC         OU       MC     MU    CCSC   CCSU   YGC     YGCT    FGC    FGCT     GCT
 0.0    0.0    0.0    0.0   40960.0  20480.0  204800.0    4096.0  61440.0 59900.3 8064.0 7678.7     5    0.078   0      0.000    0.078`

	stats, err := parseGCStats(out)
	if err != nil {
		t.Fatalf("parseGCStats: %v", err)
	}

	if stats.EC != 40960.0 {
		t.Errorf("EC = %v, want 40960.0", stats.EC)
	}
	if stats.EU != 20480.0 {
		t.Errorf("EU = %v, want 20480.0", stats.EU)
	}
	if stats.OC != 204800.0 {
		t.Errorf("OC = %v, want 204800.0", stats.OC)
	}
	if stats.OU != 4096.0 {
		t.Errorf("OU = %v, want 4096.0", stats.OU)
	}
	if stats.MC != 61440.0 {
		t.Errorf("MC = %v, want 61440.0", stats.MC)
	}
	if stats.MU != 59900.3 {
		t.Errorf("MU = %v, want 59900.3", stats.MU)
	}
	if stats.YGC != 5 {
		t.Errorf("YGC = %d, want 5", stats.YGC)
	}
	if stats.YGCT != 0.078 {
		t.Errorf("YGCT = %v, want 0.078", stats.YGCT)
	}
	if stats.FGC != 0 {
		t.Errorf("FGC = %d, want 0", stats.FGC)
	}
	if stats.GCT != 0.078 {
		t.Errorf("GCT = %v, want 0.078", stats.GCT)
	}
}

func TestParseClassStats(t *testing.T) {
	out := `Loaded  Bytes  Unloaded  Bytes     Time
  1234  2048.0        12    32.0     0.42
`
	got, err := parseClassStats(out)
	if err != nil {
		t.Fatalf("parseClassStats: %v", err)
	}
	if got.Loaded != 1234 || got.LoadedKiB != 2048.0 {
		t.Fatalf("loaded = %d/%v, want 1234/2048", got.Loaded, got.LoadedKiB)
	}
	if got.Unloaded != 12 || got.UnloadedKiB != 32.0 || got.ClassLoadTime != 0.42 {
		t.Fatalf("unloaded/time = %#v", got)
	}
}

func TestParseGCStatsSurvivorSpaces(t *testing.T) {
	// ParallelGC survivor spaces are non-zero.
	out := ` S0C    S1C    S0U    S1U      EC       EU        OC         OU       MC     MU    CCSC   CCSU   YGC     YGCT    FGC    FGCT     GCT
512.0  512.0  256.0    0.0   4096.0   2048.0   8192.0     1024.0  32768.0 30000.0 4096.0 3800.0    12    0.150   2      0.025    0.175`

	stats, err := parseGCStats(out)
	if err != nil {
		t.Fatalf("parseGCStats: %v", err)
	}
	if stats.S0C != 512.0 {
		t.Errorf("S0C = %v, want 512.0", stats.S0C)
	}
	if stats.S0U != 256.0 {
		t.Errorf("S0U = %v, want 256.0", stats.S0U)
	}
	if stats.YGC != 12 {
		t.Errorf("YGC = %d, want 12", stats.YGC)
	}
	if stats.FGC != 2 {
		t.Errorf("FGC = %d, want 2", stats.FGC)
	}
}

func TestParseGCStatsTooFewLines(t *testing.T) {
	if _, err := parseGCStats(""); err == nil {
		t.Error("expected error for empty input")
	}
	if _, err := parseGCStats("S0C S1C"); err == nil {
		t.Error("expected error for single-line input")
	}
}

func TestCollectGCStatsFakeRunner(t *testing.T) {
	var capturedArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		capturedArgs = append([]string{name}, args...)
		return []byte(` S0C    S1C    S0U    S1U      EC       EU        OC         OU       MC     MU    CCSC   CCSU   YGC     YGCT    FGC    FGCT     GCT
 0.0    0.0    0.0    0.0   1024.0    512.0   4096.0     256.0  8192.0  7000.0 1024.0  900.0     3    0.030   0      0.000    0.030`), nil
	}
	stats, err := CollectGCStats(42, run)
	if err != nil {
		t.Fatalf("CollectGCStats: %v", err)
	}
	if capturedArgs[0] != "jstat" || capturedArgs[1] != "-gc" || capturedArgs[2] != "42" {
		t.Errorf("unexpected command: %v", capturedArgs)
	}
	if stats.YGC != 3 {
		t.Errorf("YGC = %d, want 3", stats.YGC)
	}
}
