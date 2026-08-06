package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
)

// sampleThreadDump is a minimal jstack-style dump: two application threads plus
// a Java-level deadlock section, enough to exercise parse -> summarize -> render.
const sampleThreadDump = `2024-01-01 00:00:00
Full thread dump OpenJDK 64-Bit Server VM:

"worker-1" #12 daemon prio=5 os_prio=0 tid=0x1 nid=0xa waiting for monitor entry [0x1]
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.A.run(A.java:10)
	- waiting to lock <0x0000000700000001> (a java.lang.Object)

"worker-2" #13 daemon prio=5 os_prio=0 tid=0x2 nid=0xb waiting for monitor entry [0x2]
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.B.run(B.java:20)
	- waiting to lock <0x0000000700000002> (a java.lang.Object)

Found one Java-level deadlock:
=============================
"worker-1":
  waiting to lock monitor 0x1 (object 0x0000000700000001, a java.lang.Object),
  which is held by "worker-2"
"worker-2":
  waiting to lock monitor 0x2 (object 0x0000000700000002, a java.lang.Object),
  which is held by "worker-1"
`

func TestThreadDumpSummaryRendersDeadlock(t *testing.T) {
	dump := jvm.ParseThreadDump(sampleThreadDump, time.Unix(0, 0))
	sum := jvm.SummarizeThreads(dump)

	if len(dump.Deadlocks) == 0 {
		t.Fatalf("expected a deadlock cycle to be parsed, got none")
	}
	if sum.Total < 2 {
		t.Fatalf("expected >=2 threads summarized, got %d", sum.Total)
	}

	var buf bytes.Buffer
	printThreadSummary(&buf, 4012, sum, dump.Deadlocks)
	out := buf.String()
	if !strings.Contains(out, "Thread dump for PID 4012") {
		t.Fatalf("summary missing header:\n%s", out)
	}
	if !strings.Contains(out, "DEADLOCKS:") {
		t.Fatalf("summary did not surface deadlocks:\n%s", out)
	}
}

func TestThreadDumpReportJSONRoundTrip(t *testing.T) {
	dump := jvm.ParseThreadDump(sampleThreadDump, time.Unix(0, 0))
	sum := jvm.SummarizeThreads(dump)

	data, err := json.Marshal(threadDumpReport{Summary: sum, Dump: dump})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var back threadDumpReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if back.Summary.Total != sum.Total {
		t.Fatalf("round-trip total = %d, want %d", back.Summary.Total, sum.Total)
	}
	if len(back.Dump.Deadlocks) != len(dump.Deadlocks) {
		t.Fatalf("round-trip deadlocks = %d, want %d", len(back.Dump.Deadlocks), len(dump.Deadlocks))
	}
}
