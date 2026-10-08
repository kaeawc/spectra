package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/heapdump"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func heapDumpSnap(dumps ...snapshot.HeapDump) snapshot.Snapshot {
	s := baseSnap()
	s.JVMs = []jvm.Info{{PID: 10, MainClass: "svc.App"}}
	s.HeapDumps = dumps
	return s
}

func TestJVMHeapDumpFoundLinksDumpToPID(t *testing.T) {
	mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := heapDumpSnap(snapshot.HeapDump{PID: 10, MainClass: "svc.App", Dump: heapdump.Dump{
		Path: "/dumps/java_pid10.hprof", SizeBytes: 3 << 30, ModTime: mtime,
		Source: heapdump.SourceHeapDumpPath, DumpPID: 10,
	}})
	findings := ruleJVMHeapDumpFound().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %v", findings)
	}
	f := findings[0]
	if f.RuleID != "jvm-heap-dump-found" || f.Severity != SeverityMedium {
		t.Fatalf("finding = %+v", f)
	}
	if !strings.Contains(f.Subject, "PID 10") || !strings.Contains(f.Subject, "/dumps/java_pid10.hprof") {
		t.Fatalf("subject = %q", f.Subject)
	}
	for _, want := range []string{"3.0 GiB", "2026-10-01T12:00:00Z", "written by this JVM"} {
		if !strings.Contains(f.Message, want) {
			t.Fatalf("message %q missing %q", f.Message, want)
		}
	}
	if !strings.Contains(f.Fix, "spectra jvm heap-hprof /dumps/java_pid10.hprof") {
		t.Fatalf("fix = %q", f.Fix)
	}
}

func TestJVMHeapDumpFoundEarlierProcess(t *testing.T) {
	s := heapDumpSnap(snapshot.HeapDump{PID: 10, Dump: heapdump.Dump{
		Path: "/srv/java_pid7.hprof", Source: heapdump.SourceWorkingDir, DumpPID: 7,
	}})
	findings := ruleJVMHeapDumpFound().MatchFn(s)
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "earlier JVM (PID 7, no longer running)") {
		t.Fatalf("findings = %v", findings)
	}
}

func TestJVMHeapDumpFoundCustomName(t *testing.T) {
	s := heapDumpSnap(snapshot.HeapDump{PID: 10, Dump: heapdump.Dump{
		Path: "/dumps/oom.hprof", Source: heapdump.SourceHeapDumpPath,
	}})
	findings := ruleJVMHeapDumpFound().MatchFn(s)
	if len(findings) != 1 || !strings.Contains(findings[0].Message, "-XX:HeapDumpPath") {
		t.Fatalf("findings = %v", findings)
	}
}

func TestJVMHeapDumpFoundNoDumps(t *testing.T) {
	if f := ruleJVMHeapDumpFound().MatchFn(heapDumpSnap()); len(f) != 0 {
		t.Fatalf("expected no findings, got %v", f)
	}
}
