package jvm

import (
	"reflect"
	"testing"
	"time"
)

const deadlockedDump = `2026-05-07 10:00:00
Full thread dump OpenJDK 64-Bit Server VM:

"DeadlockDetector" #2 prio=5 os_prio=31 tid=0x2 nid=0x102 runnable [0x0]
   java.lang.Thread.State: RUNNABLE

"worker-1" #12 prio=5 os_prio=31 tid=0xc nid=0x10c waiting for monitor entry [0x0]
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.A.run(A.java:10)
	- waiting to lock <0x0000000700000001> (a java.lang.Object)
	- locked <0x0000000700000002> (a java.lang.Object)

"worker-2" #13 prio=5 os_prio=31 tid=0xd nid=0x10d waiting for monitor entry [0x0]
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.B.run(B.java:20)
	- waiting to lock <0x0000000700000002> (a java.lang.Object)
	- locked <0x0000000700000001> (a java.lang.Object)

Found one Java-level deadlock:
=============================
"worker-1":
  waiting to lock monitor 0x00007f8b0c006b00 (object 0x0000000700000001, a java.lang.Object),
  which is held by "worker-2"

"worker-2":
  waiting to lock monitor 0x00007f8b0c004200 (object 0x0000000700000002, a java.lang.Object),
  which is held by "worker-1"

Java stack information for the threads listed above:
===================================================
"worker-1":
	at com.example.A.run(A.java:10)
	- waiting to lock <0x0000000700000001> (a java.lang.Object)

Found one Java-level deadlock:
=============================
"pool-1":
  waiting for ownable synchronizer 0x0000000700000003, (a java.util.concurrent.locks.ReentrantLock$NonfairSync),
  which is held by "pool-2"

"pool-2":
  waiting for ownable synchronizer 0x0000000700000004, (a java.util.concurrent.locks.ReentrantLock$NonfairSync),
  which is held by "pool-1"

Java stack information for the threads listed above:
===================================================
"pool-1":
	at jdk.internal.misc.Unsafe.park(Native Method)

Found 2 deadlocks.
`

func TestParseThreadDumpDeadlockCycles(t *testing.T) {
	dump := ParseThreadDump(deadlockedDump, time.Unix(0, 0))
	if len(dump.Deadlocks) != 2 {
		t.Fatalf("deadlocks = %d, want 2: %+v", len(dump.Deadlocks), dump.Deadlocks)
	}
	monitor := dump.Deadlocks[0]
	if !reflect.DeepEqual(monitor.Threads, []string{"worker-1", "worker-2"}) {
		t.Fatalf("monitor cycle threads = %v", monitor.Threads)
	}
	wantWaits := []DeadlockWait{
		{Thread: "worker-1", Lock: "<0x0000000700000001> (a java.lang.Object)", HeldBy: "worker-2"},
		{Thread: "worker-2", Lock: "<0x0000000700000002> (a java.lang.Object)", HeldBy: "worker-1"},
	}
	if !reflect.DeepEqual(monitor.Waits, wantWaits) {
		t.Fatalf("monitor waits = %+v, want %+v", monitor.Waits, wantWaits)
	}
	if len(monitor.Locks) != 2 {
		t.Fatalf("monitor locks = %v", monitor.Locks)
	}
	ownable := dump.Deadlocks[1]
	if ownable.Waits[0].Lock != "<0x0000000700000003> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)" ||
		ownable.Waits[0].HeldBy != "pool-2" {
		t.Fatalf("ownable wait = %+v", ownable.Waits[0])
	}
}

func TestParseThreadDumpNoDeadlockSection(t *testing.T) {
	// A thread or frame mentioning "deadlock" must not produce a phantom cycle.
	dump := ParseThreadDump(`"DeadlockDetector" #2 prio=5 tid=0x2 nid=0x102 runnable [0x0]
   java.lang.Thread.State: RUNNABLE
	at com.example.DeadlockDetector.run(DeadlockDetector.java:5)
`, time.Unix(0, 0))
	if len(dump.Deadlocks) != 0 {
		t.Fatalf("deadlocks = %+v, want none", dump.Deadlocks)
	}
}
