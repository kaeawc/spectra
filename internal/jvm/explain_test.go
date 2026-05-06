package jvm

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCollectExplanationInterpretsJVMState(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "jcmd":
			if len(args) < 2 {
				return nil, fmt.Errorf("jcmd needs args")
			}
			switch args[1] {
			case "VM.version":
				return []byte("OpenJDK 64-Bit Server VM version 21"), nil
			case "VM.system_properties":
				return []byte(`java.home=/jdk
java.vendor=Acme
java.version=21.0.1
`), nil
			case "VM.command_line":
				return []byte(`VM Arguments:
jvm_args: -Xmx512m -XX:+UseSerialGC -XX:+HeapDumpOnOutOfMemoryError
`), nil
			case "Thread.print":
				return []byte("\"main\" #1\n"), nil
			case "GC.heap_info":
				return []byte("tenured generation total 1000K, used 950K\n"), nil
			case "VM.metaspace":
				return []byte(`Total Usage - 12 loaders, 345 classes:
       Both: 40.00 MB capacity, 39.00 MB (>99%) committed, 38.00 MB ( 97%) used
`), nil
			case "VM.native_memory":
				return []byte("Native memory tracking is not enabled\n"), nil
			case "VM.classloader_stats":
				return []byte("Total = 12 345 41943040 39845888\n"), nil
			case "Compiler.codecache":
				return []byte(`CodeCache: size=245760Kb, used=12000Kb, max_used=13000Kb, free=233760Kb
 total_blobs=1000, nmethods=800, adapters=100, full_count=0
`), nil
			case "GC.class_histogram":
				return []byte("   7:           42           1344  java.lang.ref.SoftReference\n"), nil
			}
		case "jstat":
			return []byte(`S0C S1C S0U S1U EC EU OC OU MC MU CCSC CCSU YGC YGCT FGC FGCT GCT
0.0 0.0 0.0 0.0 100.0 20.0 1000.0 950.0 40000.0 39000.0 1000.0 900.0 5 0.078 6 1.500 1.578
`), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}

	got, err := CollectExplanation(context.Background(), 42, ExplainOptions{CmdRunner: run, SoftRefs: true})
	if err != nil {
		t.Fatalf("CollectExplanation: %v", err)
	}
	if got.PID != 42 || got.JDKVersion != "21.0.1" {
		t.Fatalf("unexpected JVM identity: %#v", got)
	}
	if got.SoftRefs == nil || got.SoftRefs.Instances != 42 {
		t.Fatalf("SoftRefs = %#v", got.SoftRefs)
	}
	for _, id := range []string{"old-gen-occupancy", "serial-gc", "metaspace-usage", "code-cache-use", "native-memory-unknown", "soft-refs-present"} {
		if !hasObservation(got.Observations, id) {
			t.Fatalf("missing observation %q in %#v", id, got.Observations)
		}
	}
}

func TestCollectExplanationTrend(t *testing.T) {
	jstatCalls := 0
	run := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "jcmd":
			if len(args) < 2 {
				return nil, fmt.Errorf("jcmd needs args")
			}
			switch args[1] {
			case "VM.version":
				return []byte("OpenJDK 64-Bit Server VM version 21"), nil
			case "VM.system_properties":
				return []byte("java.version=21\n"), nil
			case "VM.command_line", "Thread.print", "GC.heap_info", "VM.metaspace", "VM.native_memory", "VM.classloader_stats", "Compiler.codecache":
				return nil, nil
			}
		case "jstat":
			jstatCalls++
			mu := "1000.0"
			ou := "1000.0"
			if jstatCalls > 2 {
				mu = "2500.0"
				ou = "1200.0"
			}
			return []byte("S0C S1C S0U S1U EC EU OC OU MC MU CCSC CCSU YGC YGCT FGC FGCT GCT\n" +
				"0.0 0.0 0.0 0.0 100.0 20.0 10000.0 " + ou + " 4000.0 " + mu + " 1000.0 900.0 5 0.078 0 0.000 0.078\n"), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}

	got, err := CollectExplanation(context.Background(), 42, ExplainOptions{CmdRunner: run, Samples: 2})
	if err != nil {
		t.Fatalf("CollectExplanation: %v", err)
	}
	if got.Trend == nil || got.Trend.MetaspaceDeltaKiB != 1500 {
		t.Fatalf("Trend = %#v", got.Trend)
	}
	if !hasObservation(got.Observations, "resource-trend") {
		t.Fatalf("missing resource-trend: %#v", got.Observations)
	}
}

func TestParseSoftReferences(t *testing.T) {
	got := parseSoftReferences(` num     #instances         #bytes  class name
   1:          100           3200  java.lang.ref.SoftReference
   2:            3             96  java.lang.ref.WeakReference
`)
	if got.Instances != 100 || got.Bytes != 3200 {
		t.Fatalf("got %#v", got)
	}
}

func hasObservation(observations []Observation, id string) bool {
	for _, obs := range observations {
		if strings.EqualFold(obs.ID, id) {
			return true
		}
	}
	return false
}
