package rules

import "testing"

func TestParseVMArgs_Empty(t *testing.T) {
	f := ParseVMArgs("")
	if f.XmxBytes != 0 || f.XmsBytes != 0 {
		t.Fatalf("expected zero sizes, got %+v", f)
	}
	if f.MaxHeapFreeRatio != -1 || f.MinHeapFreeRatio != -1 {
		t.Fatalf("expected -1 ratios, got %+v", f)
	}
	if f.GCAlgorithm != "" || f.NMTEnabled || f.HeapDumpOnOOM {
		t.Fatalf("expected zero defaults, got %+v", f)
	}
}

func TestParseVMArgs_JetBrainsToolbox(t *testing.T) {
	args := "-Xmx190m -Xms8m -Xss384k -XX:+UnlockExperimentalVMOptions " +
		"-XX:+CreateCoredumpOnCrash -XX:MetaspaceSize=16m -XX:MinMetaspaceFreeRatio=10 " +
		"-XX:MaxMetaspaceFreeRatio=10 -XX:+UseCompressedOops -XX:+UseCompressedClassPointers " +
		"-XX:+UseSerialGC -XX:MinHeapFreeRatio=10 -XX:MaxHeapFreeRatio=10 " +
		"-XX:-ShrinkHeapInSteps -XX:+HeapDumpOnOutOfMemoryError"
	f := ParseVMArgs(args)
	if f.XmxBytes != 190*1024*1024 {
		t.Errorf("XmxBytes = %d, want %d", f.XmxBytes, 190*1024*1024)
	}
	if f.XmsBytes != 8*1024*1024 {
		t.Errorf("XmsBytes = %d, want %d", f.XmsBytes, 8*1024*1024)
	}
	if f.MaxHeapFreeRatio != 10 || f.MinHeapFreeRatio != 10 {
		t.Errorf("HeapFreeRatio = (%d,%d), want (10,10)", f.MinHeapFreeRatio, f.MaxHeapFreeRatio)
	}
	if f.MaxMetaspaceFreeRatio != 10 || f.MinMetaspaceFreeRatio != 10 {
		t.Errorf("MetaspaceFreeRatio = (%d,%d), want (10,10)", f.MinMetaspaceFreeRatio, f.MaxMetaspaceFreeRatio)
	}
	if f.GCAlgorithm != "Serial" {
		t.Errorf("GCAlgorithm = %q, want Serial", f.GCAlgorithm)
	}
	if !f.HeapDumpOnOOM {
		t.Error("HeapDumpOnOOM should be true")
	}
	if f.NMTEnabled {
		t.Error("NMTEnabled should be false (not set)")
	}
}

func TestParseVMArgs_GAlgorithms(t *testing.T) {
	cases := map[string]string{
		"-XX:+UseG1GC":         "G1",
		"-XX:+UseZGC":          "Z",
		"-XX:+UseParallelGC":   "Parallel",
		"-XX:+UseShenandoahGC": "Shenandoah",
		"-XX:+UseEpsilonGC":    "Epsilon",
	}
	for in, want := range cases {
		if got := ParseVMArgs(in).GCAlgorithm; got != want {
			t.Errorf("ParseVMArgs(%q).GCAlgorithm = %q, want %q", in, got, want)
		}
	}
}

func TestParseVMArgs_NMT(t *testing.T) {
	if !ParseVMArgs("-XX:NativeMemoryTracking=summary").NMTEnabled {
		t.Error("summary should enable NMT")
	}
	if !ParseVMArgs("-XX:NativeMemoryTracking=detail").NMTEnabled {
		t.Error("detail should enable NMT")
	}
	if ParseVMArgs("-XX:NativeMemoryTracking=off").NMTEnabled {
		t.Error("off should leave NMT disabled")
	}
}

func TestParseSizeSuffix(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"190m", 190 * 1024 * 1024},
		{"2g", 2 * 1024 * 1024 * 1024},
		{"512K", 512 * 1024},
		{"4G", 4 * 1024 * 1024 * 1024},
		{"1024", 1024}, // no suffix → bare bytes
		{"", 0},
	}
	for _, c := range cases {
		if got := parseSizeSuffix(c.in); got != c.want {
			t.Errorf("parseSizeSuffix(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestVMArgsFacts_XmxMB(t *testing.T) {
	if got := ParseVMArgs("-Xmx2g").XmxMB(); got != 2048 {
		t.Errorf("XmxMB(-Xmx2g) = %d, want 2048", got)
	}
	if got := ParseVMArgs("").XmxMB(); got != 0 {
		t.Errorf("XmxMB(empty) = %d, want 0", got)
	}
}
