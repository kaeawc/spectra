package snapshot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
)

const deadlockThreadDump = `Full thread dump OpenJDK 64-Bit Server VM:

"worker-1" #12 prio=5 tid=0xc nid=0x10c waiting for monitor entry [0x0]
   java.lang.Thread.State: BLOCKED (on object monitor)

"worker-2" #13 prio=5 tid=0xd nid=0x10d waiting for monitor entry [0x0]
   java.lang.Thread.State: BLOCKED (on object monitor)

Found one Java-level deadlock:
=============================
"worker-1":
  waiting to lock monitor 0x1 (object 0x0000000700000001, a java.lang.Object),
  which is held by "worker-2"
"worker-2":
  waiting to lock monitor 0x2 (object 0x0000000700000002, a java.lang.Object),
  which is held by "worker-1"

Java stack information for the threads listed above:
===================================================

Found 1 deadlock.
`

const healthyThreadDump = `Full thread dump OpenJDK 64-Bit Server VM:

"main" #1 prio=5 tid=0x1 nid=0x101 runnable [0x0]
   java.lang.Thread.State: RUNNABLE
`

// jcmdRecorder fakes jcmd Thread.print per PID and records which PIDs were
// dumped, so tests can assert dumps are taken only for named PIDs.
type jcmdRecorder struct {
	mu    sync.Mutex
	dumps map[string]string
	calls []string
}

func (r *jcmdRecorder) run(name string, args ...string) ([]byte, error) {
	if name != "jcmd" || len(args) != 2 || args[1] != "Thread.print" {
		return nil, errors.New("unexpected command: " + name + " " + strings.Join(args, " "))
	}
	r.mu.Lock()
	r.calls = append(r.calls, args[0])
	r.mu.Unlock()
	out, ok := r.dumps[args[0]]
	if !ok {
		return nil, errors.New("jcmd: no such process")
	}
	return []byte(out), nil
}

func TestCollectJVMDeadlocksOnlyNamedPIDs(t *testing.T) {
	rec := &jcmdRecorder{dumps: map[string]string{"10": deadlockThreadDump, "20": healthyThreadDump}}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	capturer := jvm.ThreadDumpCapturer{Run: rec.run, Now: func() time.Time { return at }}
	jvms := []jvm.Info{{PID: 10, MainClass: "svc.App"}, {PID: 20}, {PID: 30}}

	reports, warnings := collectJVMDeadlocks(context.Background(), []int{10, 20, 10, 0, 99}, jvms, capturer)

	if got := strings.Join(rec.calls, ","); got != "10,20,99" {
		t.Fatalf("jcmd calls = %q, want only the named, deduped PIDs", got)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %+v, want 2", reports)
	}
	dl := reports[0]
	if dl.PID != 10 || dl.MainClass != "svc.App" || !dl.CapturedAt.Equal(at) || len(dl.Cycles) != 1 {
		t.Fatalf("deadlock report = %+v", dl)
	}
	if got := strings.Join(dl.Cycles[0].Threads, ","); got != "worker-1,worker-2" {
		t.Fatalf("cycle threads = %q", got)
	}
	if reports[1].PID != 20 || len(reports[1].Cycles) != 0 {
		t.Fatalf("healthy report = %+v", reports[1])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "PID 99") {
		t.Fatalf("warnings = %v, want one for PID 99", warnings)
	}
}

func TestBuildCapturesThreadDumpsOnlyWhenRequested(t *testing.T) {
	base := Options{
		SpectraVersion: "test",
		SkipApps:       true,
		SkipProcesses:  true,
		SkipStorage:    true,
		SkipServices:   true,
		SkipUpdates:    true,
		SkipJVMs:       true,
	}

	rec := &jcmdRecorder{dumps: map[string]string{"10": deadlockThreadDump}}
	base.JVMOpts = jvm.CollectOptions{CmdRunner: rec.run}
	if snap := Build(context.Background(), base); len(snap.JVMDeadlocks) != 0 || len(rec.calls) != 0 {
		t.Fatalf("no thread dumps expected without ThreadDumpPIDs: calls=%v reports=%v", rec.calls, snap.JVMDeadlocks)
	}

	base.ThreadDumpPIDs = []int{10}
	snap := Build(context.Background(), base)
	if len(snap.JVMDeadlocks) != 1 || len(snap.JVMDeadlocks[0].Cycles) != 1 {
		t.Fatalf("JVMDeadlocks = %+v", snap.JVMDeadlocks)
	}
}
