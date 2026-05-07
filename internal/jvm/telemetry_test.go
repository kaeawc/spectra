package jvm

import (
	"testing"
	"time"
)

func TestTelemetryForInfoMapsGCHeapThreadsAndIdentity(t *testing.T) {
	at := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	info := Info{
		PID:         412,
		MainClass:   "com.example.Main",
		JavaHome:    "/Library/Java/JavaVirtualMachines/temurin.jdk/Contents/Home",
		JDKVendor:   "Eclipse Adoptium",
		JDKVersion:  "21.0.3",
		VMArgs:      "-Xmx2g",
		ThreadCount: 37,
		SysProps: map[string]string{
			"java.io.tmpdir": "/tmp",
		},
		GC: &GCStats{
			EC:   1024,
			EU:   512,
			OC:   2048,
			OU:   256,
			YGC:  7,
			YGCT: 0.125,
			FGC:  1,
			FGCT: 0.250,
		},
	}

	got := TelemetryForInfo(info, TelemetryOptions{Clock: func() time.Time { return at }})

	if got.PID != 412 {
		t.Fatalf("PID = %d, want 412", got.PID)
	}
	if got.Identity["main_class"] != "com.example.Main" {
		t.Fatalf("main_class = %q", got.Identity["main_class"])
	}
	if got.Config["vm_args"] != "-Xmx2g" {
		t.Fatalf("vm_args = %q", got.Config["vm_args"])
	}
	if got.Config["sysprop.java.io.tmpdir"] != "/tmp" {
		t.Fatalf("tmpdir = %q", got.Config["sysprop.java.io.tmpdir"])
	}
	if got.Threads == nil || got.Threads.Live != 37 {
		t.Fatalf("Threads = %#v, want live 37", got.Threads)
	}
	if got.GC == nil || got.GC.Collections != 8 || got.GC.CollectionTimeMS != 375 {
		t.Fatalf("GC = %#v, want 8 collections and 375ms", got.GC)
	}
	if got.Heap == nil || got.Heap.UsedBytes != 768*1024 {
		t.Fatalf("Heap = %#v, want 768KiB used", got.Heap)
	}
	if !got.Collected.Equal(at) {
		t.Fatalf("Collected = %v, want %v", got.Collected, at)
	}
}
