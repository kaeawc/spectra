package jvm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/telemetry"
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
		Classes: &ClassStats{Loaded: 1234},
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
	if len(got.Sections) != 1 || got.Sections[0].Name != "jvm.class_stats" {
		t.Fatalf("Sections = %#v, want class stats", got.Sections)
	}
	if !got.Collected.Equal(at) {
		t.Fatalf("Collected = %v, want %v", got.Collected, at)
	}
}

func TestBuildTelemetrySampleFromInfoAndProbes(t *testing.T) {
	at := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	info := Info{
		PID:         42,
		MainClass:   "example.Main",
		ThreadCount: 12,
		GC: &GCStats{
			EC: 1000, EU: 400, OC: 9000, OU: 6000,
			MC: 500, MU: 250, YGC: 3, YGCT: 0.12, FGC: 1, FGCT: 0.5, GCT: 0.62,
		},
		Classes: &ClassStats{Loaded: 4321},
	}
	probes := AgentProbes{}
	probes.Runtime.MaxMemory = 1024
	probes.Threads.Live = 13
	probes.Counters = []AgentCounter{
		{Name: "loaded_classes", Value: float64(1234)},
		{Name: "virtual_threads", Value: float64(23)},
	}

	got := BuildTelemetrySample(at, info, &probes, nil)
	if got.HeapUsedKiB != 6400 || got.HeapCapacityKiB != 10000 {
		t.Fatalf("heap = %v/%v, want 6400/10000", got.HeapUsedKiB, got.HeapCapacityKiB)
	}
	if got.MetaspaceUsedKiB != 250 || got.MetaspaceCapacityKiB != 500 {
		t.Fatalf("metaspace = %v/%v, want 250/500", got.MetaspaceUsedKiB, got.MetaspaceCapacityKiB)
	}
	if !got.AgentAttached || got.AgentThreadCount != 13 || got.LoadedClassCount != 4321 || got.VirtualThreadCount != 23 {
		t.Fatalf("agent fields = %#v", got)
	}
	subject := got.TelemetrySubject()
	if subject.Kind != "process" || subject.Runtime != string(telemetry.RuntimeJVM) || subject.PID != 42 {
		t.Fatalf("subject = %#v", subject)
	}
}

func TestTelemetrySamplerCollectsVisibleJVMs(t *testing.T) {
	c := telemetry.NewLiveCollector()
	run := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "jps":
			return []byte("42 example.Main\n"), nil
		case name == "jcmd" && len(args) == 2 && args[1] == "VM.system_properties":
			return []byte("java.home=/jdk\njava.vendor=vendor\njava.version=21\n"), nil
		case name == "jcmd" && len(args) == 2 && args[1] == "VM.command_line":
			return []byte("VM Arguments:\njvm_args: -Xmx1g\n"), nil
		case name == "jcmd" && len(args) == 2 && args[1] == "Thread.print":
			return []byte("\"main\" #1\n\"worker\" #2\n"), nil
		case name == "jstat" && len(args) > 0 && args[0] == "-gc":
			return []byte("EC EU OC OU MC MU YGC YGCT FGC FGCT GCT\n100 10 200 20 30 3 1 0.1 0 0.0 0.1\n"), nil
		case name == "jstat" && len(args) > 0 && args[0] == "-class":
			return []byte("Loaded Bytes Unloaded Bytes Time\n1234 2048.0 0 0.0 0.1\n"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s %v", name, args)
	}
	s := NewTelemetrySampler(c, time.Hour, CollectOptions{CmdRunner: run}, func(pid int) (AgentProbes, error) {
		p := AgentProbes{}
		p.Runtime.MaxMemory = 4096
		p.Threads.Live = 2
		return p, nil
	})

	s.sample(context.Background(), time.Now())
	got := c.RecentPID("process", 42, 1)
	if len(got) != 1 {
		t.Fatalf("samples = %d, want 1", len(got))
	}
	sample := got[0].(TelemetrySample)
	if sample.HeapUsedKiB != 30 || sample.ThreadCount != 2 || sample.LoadedClassCount != 1234 || !sample.AgentAttached {
		t.Fatalf("sample = %#v", sample)
	}
}
