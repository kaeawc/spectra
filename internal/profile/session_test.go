package profile

import (
	"reflect"
	"testing"
	"time"
)

func TestNormalizeSessionDefaultsCPU(t *testing.T) {
	got, err := NormalizeSession(SessionSpec{
		Target: Target{PID: 42, Architecture: ArchitectureJVM},
	})
	if err != nil {
		t.Fatalf("NormalizeSession: %v", err)
	}
	if got.Collector != "async-profiler" || got.Event != "cpu" || got.Workflow != WorkflowSampling {
		t.Fatalf("defaults = %#v", got)
	}
	if got.Duration != 30*time.Second {
		t.Fatalf("duration = %v", got.Duration)
	}
	if want := []View{ViewHotMethods, ViewCallTree}; !reflect.DeepEqual(got.Views, want) {
		t.Fatalf("views = %v, want %v", got.Views, want)
	}
}

func TestNormalizeSessionSupportsNonJVMTarget(t *testing.T) {
	got, err := NormalizeSession(SessionSpec{
		Target: Target{
			PID:          42,
			Architecture: ArchitectureNative,
			BundleID:     "com.example.NativeApp",
		},
	})
	if err != nil {
		t.Fatalf("NormalizeSession: %v", err)
	}
	if got.Collector != "sample" {
		t.Fatalf("collector = %q, want sample", got.Collector)
	}
	if got.TargetPID != 42 {
		t.Fatalf("target pid compatibility field = %d", got.TargetPID)
	}
}

func TestNormalizeSessionSupportsRuntimeTargetWithoutPID(t *testing.T) {
	got, err := NormalizeSession(SessionSpec{
		Target: Target{
			Architecture: ArchitectureBrowser,
			RuntimeID:    "renderer-7",
		},
	})
	if err != nil {
		t.Fatalf("NormalizeSession: %v", err)
	}
	if got.Collector != "browser-profiler" {
		t.Fatalf("collector = %q, want browser-profiler", got.Collector)
	}
}

func TestNormalizeSessionMigratesTargetPID(t *testing.T) {
	got, err := NormalizeSession(SessionSpec{TargetPID: 42, Collector: "async-profiler"})
	if err != nil {
		t.Fatalf("NormalizeSession: %v", err)
	}
	if got.Target.PID != 42 || got.Target.Architecture != ArchitectureUnknown {
		t.Fatalf("target = %#v", got.Target)
	}
}

func TestNormalizeSessionDefaultViewsForLock(t *testing.T) {
	got, err := NormalizeSession(SessionSpec{Target: Target{PID: 42}, Event: "lock"})
	if err != nil {
		t.Fatalf("NormalizeSession: %v", err)
	}
	want := []View{ViewLockContention, ViewHotMethods, ViewCallTree}
	if !reflect.DeepEqual(got.Views, want) {
		t.Fatalf("views = %v, want %v", got.Views, want)
	}
}

func TestNormalizeSessionRejectsMissingPID(t *testing.T) {
	if _, err := NormalizeSession(SessionSpec{}); err == nil {
		t.Fatal("expected missing pid error")
	}
}
