package crashreport

import (
	"errors"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/threadinspect"
)

// synthetic .ips: a JSON header line + JSON body. No real user data.
const sampleIPS = `{"app_name":"MyApp","app_version":"2.1","bundleID":"com.example.myapp","timestamp":"2026-08-05 10:15:00.00 -0500","os_version":"macOS 15.6.1 (24G90)","bug_type":"309","incident_id":"ABC-123","name":"MyApp"}
{"procName":"MyApp","procPath":"/Applications/MyApp.app/Contents/MacOS/MyApp","pid":4012,"exception":{"type":"EXC_BAD_ACCESS","signal":"SIGSEGV","codes":"0x1, 0x0"},"termination":{"namespace":"SIGNAL","indicator":"Segmentation fault: 11","code":11},"faultingThread":1,"threads":[{"id":100,"queue":"com.apple.main-thread","frames":[{"imageIndex":0,"imageOffset":1000,"symbol":"main","symbolLocation":42}]},{"id":101,"queue":"sync.queue","triggered":true,"frames":[{"imageIndex":0,"imageOffset":171856,"symbol":"-[SyncEngine flush]","symbolLocation":214},{"imageIndex":1,"imageOffset":5000}]}],"usedImages":[{"name":"MyApp","base":4294967296,"arch":"arm64","path":"/Applications/MyApp.app/Contents/MacOS/MyApp"},{"name":"libdispatch.dylib","base":1,"arch":"arm64"}]}`

func TestParseHeaderAndException(t *testing.T) {
	r, err := Parse([]byte(sampleIPS))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Process != "MyApp" {
		t.Errorf("Process = %q, want MyApp", r.Process)
	}
	if r.Kind != "crash" {
		t.Errorf("Kind = %q, want crash", r.Kind)
	}
	if r.Exception != "EXC_BAD_ACCESS (SIGSEGV)" {
		t.Errorf("Exception = %q", r.Exception)
	}
	if !strings.Contains(r.ExceptionDetail, "segmentation") {
		t.Errorf("ExceptionDetail = %q, want segmentation-fault explanation", r.ExceptionDetail)
	}
	if r.Termination != "Segmentation fault: 11" {
		t.Errorf("Termination = %q", r.Termination)
	}
	if r.FaultingThread != 1 {
		t.Errorf("FaultingThread = %d, want 1", r.FaultingThread)
	}
	if r.PID != 4012 {
		t.Errorf("PID = %d, want 4012", r.PID)
	}
}

func TestParseFrameResolution(t *testing.T) {
	r, err := Parse([]byte(sampleIPS))
	if err != nil {
		t.Fatal(err)
	}
	faulting := r.Threads[1]
	if !faulting.Triggered {
		t.Error("thread 1 should be triggered")
	}
	// symbolized frame uses the embedded symbol + location
	if got, want := faulting.Frames[0], "MyApp`-[SyncEngine flush] + 214"; got != want {
		t.Errorf("frame[0] = %q, want %q", got, want)
	}
	// unsymbolized frame falls back to image + hex offset (5000 = 0x1388)
	if got, want := faulting.Frames[1], "libdispatch.dylib + 0x1388"; got != want {
		t.Errorf("frame[1] = %q, want %q", got, want)
	}
}

func TestSnapshotNormalization(t *testing.T) {
	r, err := Parse([]byte(sampleIPS))
	if err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.Runtime != threadinspect.RuntimeNative {
		t.Errorf("Runtime = %q, want native", s.Runtime)
	}
	if s.PID != 4012 {
		t.Errorf("PID = %d", s.PID)
	}
	if len(s.Threads) != 2 {
		t.Fatalf("threads = %d, want 2", len(s.Threads))
	}
	if !strings.Contains(s.Threads[1].Name, "(faulting)") {
		t.Errorf("faulting thread name = %q, want (faulting) marker", s.Threads[1].Name)
	}
	if s.Threads[1].TopFrame == "" {
		t.Error("faulting thread should have a top frame")
	}
}

func TestParseLegacyFormat(t *testing.T) {
	legacy := []byte("Process:         Foo [123]\nIdentifier:      com.example.foo\n")
	_, err := Parse(legacy)
	if !errors.Is(err, ErrLegacyFormat) {
		t.Fatalf("err = %v, want ErrLegacyFormat", err)
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse([]byte("   ")); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParseNoBody(t *testing.T) {
	if _, err := Parse([]byte(`{"app_name":"X"}`)); err == nil {
		t.Fatal("expected error for header with no body line")
	}
}

func TestCrashKind(t *testing.T) {
	cases := map[string]string{
		"309": "crash",
		"288": "hang",
		"":    "crash report",
		"999": "crash report (bug_type 999)",
	}
	for in, want := range cases {
		if got := crashKind(in); got != want {
			t.Errorf("crashKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExplainException(t *testing.T) {
	if explainException("EXC_BAD_ACCESS") == "" {
		t.Error("EXC_BAD_ACCESS should have an explanation")
	}
	if explainException("EXC_TOTALLY_UNKNOWN") != "" {
		t.Error("unknown exception types should return empty")
	}
}
