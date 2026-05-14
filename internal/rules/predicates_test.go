package rules

import (
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestOldGenUsedPct(t *testing.T) {
	cases := []struct {
		name string
		j    jvm.Info
		want float64
	}{
		{"nil GC", jvm.Info{}, 0},
		{"zero capacity", jvm.Info{GC: &jvm.GCStats{OC: 0, OU: 100}}, 0},
		{"half full", jvm.Info{GC: &jvm.GCStats{OC: 1000, OU: 500}}, 50},
		{"96 percent", jvm.Info{GC: &jvm.GCStats{OC: 1000, OU: 960}}, 96},
	}
	for _, c := range cases {
		if got := OldGenUsedPct(c.j); got != c.want {
			t.Errorf("%s: OldGenUsedPct = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOldGenHigh(t *testing.T) {
	if !OldGenHigh(jvm.Info{GC: &jvm.GCStats{OC: 1000, OU: 925}}) {
		t.Error("92.5% should be high")
	}
	if OldGenHigh(jvm.Info{GC: &jvm.GCStats{OC: 1000, OU: 800}}) {
		t.Error("80% should not be high")
	}
}

func TestTightHeapByDesign(t *testing.T) {
	if !TightHeapByDesign(ParseVMArgs("-XX:MaxHeapFreeRatio=10")) {
		t.Error("MaxHeapFreeRatio=10 (Toolbox) should be tight by design")
	}
	if !TightHeapByDesign(ParseVMArgs("-XX:MaxHeapFreeRatio=20")) {
		t.Error("MaxHeapFreeRatio=20 (boundary) should be tight by design")
	}
	if TightHeapByDesign(ParseVMArgs("-XX:MaxHeapFreeRatio=70")) {
		t.Error("MaxHeapFreeRatio=70 (default G1) should NOT be tight by design")
	}
	if TightHeapByDesign(ParseVMArgs("")) {
		t.Error("absent flag should NOT be tight by design")
	}
}

func TestTightMetaspaceByDesign(t *testing.T) {
	if !TightMetaspaceByDesign(ParseVMArgs("-XX:MaxMetaspaceFreeRatio=10")) {
		t.Error("MaxMetaspaceFreeRatio=10 should be tight by design")
	}
	if TightMetaspaceByDesign(ParseVMArgs("")) {
		t.Error("absent flag should NOT be tight by design")
	}
}

func TestFullGCBurst(t *testing.T) {
	if FullGCBurst(jvm.Info{}) {
		t.Error("nil GC should not be a burst")
	}
	if FullGCBurst(jvm.Info{GC: &jvm.GCStats{FGC: 4, FGCT: 2.0}}) {
		t.Error("4 GCs should not be a burst")
	}
	if !FullGCBurst(jvm.Info{GC: &jvm.GCStats{FGC: 7, FGCT: 1.8}}) {
		t.Error("7 GCs / 1.8s should be a burst")
	}
	if FullGCBurst(jvm.Info{GC: &jvm.GCStats{FGC: 7, FGCT: 0.5}}) {
		t.Error("7 GCs / 0.5s should NOT be a burst (pause too small)")
	}
}
