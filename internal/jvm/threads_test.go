package jvm

import (
	"testing"
	"time"
)

const sampleThreadDump = `2026-05-07 10:00:00
Full thread dump OpenJDK 64-Bit Server VM:

"main" #1 prio=5 os_prio=31 cpu=12.34ms elapsed=1.23s tid=0x1 nid=0x101 runnable [0x0]
   java.lang.Thread.State: RUNNABLE
	at com.example.Main.run(Main.java:10)

"worker" #7 daemon prio=5 os_prio=31 cpu=1.00ms elapsed=1.23s tid=0x7 nid=0x107 waiting on condition [0x0]
   java.lang.Thread.State: TIMED_WAITING (parking)
	at jdk.internal.misc.Unsafe.park(Native Method)
	- parking to wait for  <0x0000000700012340> (a java.util.concurrent.locks.AbstractQueuedSynchronizer$ConditionObject)

"blocked" #8 prio=5 os_prio=31 cpu=0.50ms elapsed=1.23s tid=0x8 nid=0x108 waiting for monitor entry [0x0]
   java.lang.Thread.State: BLOCKED (on object monitor)
	- waiting to lock <0x00000007000abc00> (a java.lang.Object) owned by "owner" Id=9
	at com.example.Locked.enter(Locked.java:20)

"virtual-task" #11 virtual prio=5 os_prio=31 cpu=0.10ms elapsed=1.23s tid=0x11 nid=0x111 waiting on condition [0x0]
   java.lang.Thread.State: WAITING (parking)
	at java.base/java.lang.VirtualThread.park(VirtualThread.java:582)
`

func TestParseThreadDumpExtractsReusableThreadSignals(t *testing.T) {
	capturedAt := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	dump := ParseThreadDump(sampleThreadDump, capturedAt)
	if len(dump.Threads) != 4 {
		t.Fatalf("threads = %d, want 4", len(dump.Threads))
	}
	if !dump.CapturedAt.Equal(capturedAt) {
		t.Fatalf("capturedAt = %v, want %v", dump.CapturedAt, capturedAt)
	}

	main := dump.Threads[0]
	if main.Name != "main" || main.RuntimeID != "1" || main.NativeID != "0x101" {
		t.Fatalf("unexpected main identity: %#v", main)
	}
	if main.State != ThreadStateRunnable || main.Category != WaitCategoryRunning {
		t.Fatalf("main state/category = %s/%s", main.State, main.Category)
	}
	if main.TopFrame != "com.example.Main.run(Main.java:10)" {
		t.Fatalf("main top frame = %q", main.TopFrame)
	}

	parked := dump.Threads[1]
	if parked.Category != WaitCategoryPark || parked.Lock != "<0x0000000700012340>" {
		t.Fatalf("parked category/lock = %s/%q", parked.Category, parked.Lock)
	}
	if !parked.Daemon {
		t.Fatal("worker should be marked daemon")
	}

	blocked := dump.Threads[2]
	if blocked.Category != WaitCategoryMonitor || blocked.LockOwner != "owner" || blocked.LockOwnerID != "9" {
		t.Fatalf("blocked lock owner fields: %#v", blocked)
	}

	virtual := dump.Threads[3]
	if !virtual.Virtual || virtual.Category != WaitCategoryPark {
		t.Fatalf("virtual thread fields: %#v", virtual)
	}
}

func TestSummarizeAndFilterThreads(t *testing.T) {
	dump := ParseThreadDump(sampleThreadDump, time.Time{})
	summary := SummarizeThreads(dump)
	if summary.Total != 4 || summary.Daemon != 1 || summary.Virtual != 1 {
		t.Fatalf("summary totals: %#v", summary)
	}
	if summary.ByCategory[WaitCategoryRunning] != 1 ||
		summary.ByCategory[WaitCategoryPark] != 2 ||
		summary.ByCategory[WaitCategoryMonitor] != 1 {
		t.Fatalf("category counts: %#v", summary.ByCategory)
	}
	if summary.MonitorLocks["<0x00000007000abc00>"] != 1 {
		t.Fatalf("monitor lock counts: %#v", summary.MonitorLocks)
	}

	virtual := FilterThreads(dump, ThreadFilter{VirtualOnly: true, IncludeDaemon: true})
	if len(virtual) != 1 || virtual[0].Name != "virtual-task" {
		t.Fatalf("virtual filter: %#v", virtual)
	}

	parkedNonDaemon := FilterThreads(dump, ThreadFilter{Categories: []WaitCategory{WaitCategoryPark}})
	if len(parkedNonDaemon) != 1 || parkedNonDaemon[0].Name != "virtual-task" {
		t.Fatalf("parked non-daemon filter: %#v", parkedNonDaemon)
	}
}

func TestDiffThreadDumpsShowsWhatChanged(t *testing.T) {
	before := ParseThreadDump(sampleThreadDump, time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC))
	after := ParseThreadDump(`"main" #1 prio=5 os_prio=31 cpu=12.34ms elapsed=2.23s tid=0x1 nid=0x101 waiting on condition [0x0]
   java.lang.Thread.State: TIMED_WAITING (sleeping)
	at java.base/java.lang.Thread.sleep(Native Method)

"new-worker" #12 prio=5 os_prio=31 cpu=0.01ms elapsed=0.10s tid=0x12 nid=0x112 runnable [0x0]
   java.lang.Thread.State: RUNNABLE
	at com.example.Work.run(Work.java:1)
`, time.Date(2026, 5, 7, 10, 0, 5, 0, time.UTC))

	diff := DiffThreadDumps(before, after)
	if len(diff.Added) != 1 || diff.Added[0].Name != "new-worker" {
		t.Fatalf("added: %#v", diff.Added)
	}
	if len(diff.Removed) != 3 {
		t.Fatalf("removed = %d, want 3", len(diff.Removed))
	}
	if len(diff.StateChanged) != 1 || diff.StateChanged[0].Name != "main" {
		t.Fatalf("state changed: %#v", diff.StateChanged)
	}
	if diff.StateChanged[0].BeforeCat != WaitCategoryRunning || diff.StateChanged[0].AfterCat != WaitCategorySleeping {
		t.Fatalf("main category change: %#v", diff.StateChanged[0])
	}
	if diff.CategoryDelta[WaitCategoryPark] != -2 || diff.CategoryDelta[WaitCategorySleeping] != 1 {
		t.Fatalf("category delta: %#v", diff.CategoryDelta)
	}
}

func TestBuildThreadTimeline(t *testing.T) {
	first := ParseThreadDump(sampleThreadDump, time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC))
	second := ParseThreadDump(`"main" #1 prio=5 os_prio=31 cpu=12.34ms elapsed=2.23s tid=0x1 nid=0x101 waiting on condition [0x0]
   java.lang.Thread.State: TIMED_WAITING (sleeping)
`, time.Date(2026, 5, 7, 10, 0, 5, 0, time.UTC))

	timeline := BuildThreadTimeline([]ParsedThreadDump{first, second})
	if len(timeline.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(timeline.Points))
	}
	if timeline.Points[0].ByCategory[WaitCategoryPark] != 2 {
		t.Fatalf("first point categories: %#v", timeline.Points[0].ByCategory)
	}
	if timeline.Points[1].Total != 1 || timeline.Points[1].ByCategory[WaitCategorySleeping] != 1 {
		t.Fatalf("second point: %#v", timeline.Points[1])
	}
}
