package threadinspect

import (
	"testing"
	"time"
)

func TestRuntimeNeutralSummaryFilterDiffAndTimeline(t *testing.T) {
	first := Snapshot{
		Runtime:    RuntimeNative,
		PID:        42,
		CapturedAt: time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC),
		Threads: []Thread{
			{Name: "main", RuntimeID: "1", NativeID: "0x1", State: StateRunnable, Category: WaitCategoryRunning, TopFrame: "main"},
			{Name: "worker", RuntimeID: "2", NativeID: "0x2", State: StateWaiting, Category: WaitCategoryWait, TopFrame: "wait", Daemon: true},
			{Name: "fiber", RuntimeID: "3", State: StateWaiting, Category: WaitCategoryPark, Virtual: true, TopFrame: "park"},
		},
	}
	second := Snapshot{
		Runtime:    RuntimeNative,
		PID:        42,
		CapturedAt: time.Date(2026, 5, 7, 10, 0, 5, 0, time.UTC),
		Threads: []Thread{
			{Name: "main", RuntimeID: "1", NativeID: "0x1", State: StateTimedWaiting, Category: WaitCategorySleeping, TopFrame: "sleep"},
			{Name: "new-worker", RuntimeID: "4", NativeID: "0x4", State: StateRunnable, Category: WaitCategoryRunning, TopFrame: "work"},
		},
	}

	summary := Summarize(first)
	if summary.Total != 3 || summary.Daemon != 1 || summary.Virtual != 1 {
		t.Fatalf("summary totals: %#v", summary)
	}
	if summary.ByCategory[WaitCategoryRunning] != 1 || summary.ByCategory[WaitCategoryPark] != 1 {
		t.Fatalf("summary categories: %#v", summary.ByCategory)
	}

	virtual := FilterThreads(first, Filter{VirtualOnly: true, IncludeDaemon: true})
	if len(virtual) != 1 || virtual[0].Name != "fiber" {
		t.Fatalf("virtual filter: %#v", virtual)
	}

	diff := DiffSnapshots(first, second)
	if len(diff.Added) != 1 || diff.Added[0].Name != "new-worker" {
		t.Fatalf("added: %#v", diff.Added)
	}
	if len(diff.Removed) != 2 {
		t.Fatalf("removed = %d, want 2", len(diff.Removed))
	}
	if len(diff.StateChanged) != 1 || diff.StateChanged[0].AfterCat != WaitCategorySleeping {
		t.Fatalf("state changed: %#v", diff.StateChanged)
	}

	timeline := BuildTimeline([]Snapshot{first, second})
	if len(timeline.Points) != 2 {
		t.Fatalf("timeline points = %d, want 2", len(timeline.Points))
	}
	if timeline.Points[1].ByCategory[WaitCategorySleeping] != 1 {
		t.Fatalf("second point categories: %#v", timeline.Points[1].ByCategory)
	}
}

func TestRuntimeIDFromInt(t *testing.T) {
	if got := RuntimeIDFromInt(17); got != "17" {
		t.Fatalf("RuntimeIDFromInt(17) = %q", got)
	}
	if got := RuntimeIDFromInt(0); got != "" {
		t.Fatalf("RuntimeIDFromInt(0) = %q", got)
	}
}
