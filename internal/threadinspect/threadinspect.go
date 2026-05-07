// Package threadinspect defines runtime-neutral thread inspection models and
// analysis helpers. Runtime-specific packages adapt their native dump formats
// into these types.
package threadinspect

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runtime identifies the application architecture or runtime that produced a
// thread snapshot.
type Runtime string

const (
	RuntimeUnknown Runtime = "unknown"
	RuntimeJVM     Runtime = "jvm"
	RuntimeProcess Runtime = "process"
	RuntimeGo      Runtime = "go"
	RuntimeNode    Runtime = "node"
	RuntimeNative  Runtime = "native"
)

// Capturer captures a runtime-specific thread snapshot for a process.
type Capturer interface {
	CaptureThreads(ctx context.Context, pid int) (Snapshot, error)
}

// Parser adapts raw runtime-specific thread data into a Snapshot.
type Parser interface {
	ParseThreads(raw []byte, capturedAt time.Time) (Snapshot, error)
}

// State is the normalized thread execution state.
type State string

const (
	StateUnknown      State = "UNKNOWN"
	StateRunnable     State = "RUNNABLE"
	StateBlocked      State = "BLOCKED"
	StateWaiting      State = "WAITING"
	StateTimedWaiting State = "TIMED_WAITING"
	StateTerminated   State = "TERMINATED"
)

// WaitCategory groups runtime-specific states into triage views that apply
// across application architectures.
type WaitCategory string

const (
	WaitCategoryRunning  WaitCategory = "running"
	WaitCategorySleeping WaitCategory = "sleeping"
	WaitCategoryWait     WaitCategory = "wait"
	WaitCategoryPark     WaitCategory = "park"
	WaitCategoryMonitor  WaitCategory = "monitor"
	WaitCategoryUnknown  WaitCategory = "unknown"
)

// Snapshot is a structured representation of one thread capture.
type Snapshot struct {
	Runtime    Runtime         `json:"runtime,omitempty"`
	PID        int             `json:"pid,omitempty"`
	CapturedAt time.Time       `json:"captured_at,omitempty"`
	Threads    []Thread        `json:"threads"`
	Deadlocks  []DeadlockCycle `json:"deadlocks,omitempty"`
}

// Thread is one thread entry from any supported runtime.
type Thread struct {
	Name        string       `json:"name"`
	RuntimeID   string       `json:"runtime_id,omitempty"`
	NativeID    string       `json:"native_id,omitempty"`
	Daemon      bool         `json:"daemon,omitempty"`
	Virtual     bool         `json:"virtual,omitempty"`
	State       State        `json:"state,omitempty"`
	Category    WaitCategory `json:"category,omitempty"`
	Detail      string       `json:"detail,omitempty"`
	Lock        string       `json:"lock,omitempty"`
	LockOwner   string       `json:"lock_owner,omitempty"`
	LockOwnerID string       `json:"lock_owner_id,omitempty"`
	TopFrame    string       `json:"top_frame,omitempty"`
	Stack       []string     `json:"stack,omitempty"`
	RawHeader   string       `json:"raw_header,omitempty"`
	RawBlock    string       `json:"raw_block,omitempty"`
}

// DeadlockCycle describes one detected lock cycle.
type DeadlockCycle struct {
	Threads []string `json:"threads"`
	Locks   []string `json:"locks,omitempty"`
}

// Summary aggregates one parsed thread snapshot.
type Summary struct {
	Total        int                  `json:"total"`
	Daemon       int                  `json:"daemon"`
	Virtual      int                  `json:"virtual"`
	ByState      map[State]int        `json:"by_state,omitempty"`
	ByCategory   map[WaitCategory]int `json:"by_category,omitempty"`
	ByTopFrame   map[string]int       `json:"by_top_frame,omitempty"`
	MonitorLocks map[string]int       `json:"monitor_locks,omitempty"`
	Deadlocks    int                  `json:"deadlocks,omitempty"`
}

// Filter selects threads from a parsed snapshot. Empty fields do not filter.
type Filter struct {
	NameContains     string
	StackContains    string
	States           []State
	Categories       []WaitCategory
	VirtualOnly      bool
	IncludeDaemon    bool
	LockContains     string
	TopFrameContains string
}

// Diff compares two parsed snapshots.
type Diff struct {
	Added         []Thread             `json:"added,omitempty"`
	Removed       []Thread             `json:"removed,omitempty"`
	StateChanged  []Change             `json:"state_changed,omitempty"`
	CategoryDelta map[WaitCategory]int `json:"category_delta,omitempty"`
	StateDelta    map[State]int        `json:"state_delta,omitempty"`
	DeadlockDelta int                  `json:"deadlock_delta,omitempty"`
}

// Change records a state/category transition for a stable thread.
type Change struct {
	Name        string       `json:"name"`
	RuntimeID   string       `json:"runtime_id,omitempty"`
	NativeID    string       `json:"native_id,omitempty"`
	BeforeState State        `json:"before_state,omitempty"`
	AfterState  State        `json:"after_state,omitempty"`
	BeforeCat   WaitCategory `json:"before_category,omitempty"`
	AfterCat    WaitCategory `json:"after_category,omitempty"`
	BeforeTop   string       `json:"before_top_frame,omitempty"`
	AfterTop    string       `json:"after_top_frame,omitempty"`
}

// Timeline is an aggregate state timeline over repeated snapshots.
type Timeline struct {
	Points []TimelinePoint `json:"points"`
}

// TimelinePoint is one aggregate point in a thread timeline.
type TimelinePoint struct {
	CapturedAt time.Time            `json:"captured_at,omitempty"`
	Total      int                  `json:"total"`
	ByCategory map[WaitCategory]int `json:"by_category,omitempty"`
	ByState    map[State]int        `json:"by_state,omitempty"`
	Virtual    int                  `json:"virtual,omitempty"`
	Deadlocks  int                  `json:"deadlocks,omitempty"`
}

// Summarize returns reusable aggregate counts for a parsed snapshot.
func Summarize(snapshot Snapshot) Summary {
	summary := Summary{
		Total:        len(snapshot.Threads),
		ByState:      make(map[State]int),
		ByCategory:   make(map[WaitCategory]int),
		ByTopFrame:   make(map[string]int),
		MonitorLocks: make(map[string]int),
		Deadlocks:    len(snapshot.Deadlocks),
	}
	for _, thread := range snapshot.Threads {
		if thread.Daemon {
			summary.Daemon++
		}
		if thread.Virtual {
			summary.Virtual++
		}
		summary.ByState[thread.State]++
		summary.ByCategory[thread.Category]++
		if thread.TopFrame != "" {
			summary.ByTopFrame[thread.TopFrame]++
		}
		if thread.Category == WaitCategoryMonitor && thread.Lock != "" {
			summary.MonitorLocks[thread.Lock]++
		}
	}
	return summary
}

// FilterThreads returns threads matching filter.
func FilterThreads(snapshot Snapshot, filter Filter) []Thread {
	var out []Thread
	for _, thread := range snapshot.Threads {
		if threadMatches(thread, filter) {
			out = append(out, thread)
		}
	}
	return out
}

func threadMatches(thread Thread, filter Filter) bool {
	return matchesThreadKind(thread, filter) &&
		matchesThreadText(thread, filter) &&
		matchesThreadState(thread, filter)
}

func matchesThreadKind(thread Thread, filter Filter) bool {
	if !filter.IncludeDaemon && thread.Daemon {
		return false
	}
	return !filter.VirtualOnly || thread.Virtual
}

func matchesThreadText(thread Thread, filter Filter) bool {
	if filter.NameContains != "" && !containsFold(thread.Name, filter.NameContains) {
		return false
	}
	if filter.StackContains != "" && !containsFold(strings.Join(thread.Stack, "\n"), filter.StackContains) {
		return false
	}
	if filter.LockContains != "" && !containsFold(thread.Lock, filter.LockContains) {
		return false
	}
	return filter.TopFrameContains == "" || containsFold(thread.TopFrame, filter.TopFrameContains)
}

func matchesThreadState(thread Thread, filter Filter) bool {
	if len(filter.States) > 0 && !hasState(filter.States, thread.State) {
		return false
	}
	return len(filter.Categories) == 0 || hasCategory(filter.Categories, thread.Category)
}

// DiffSnapshots compares stable thread identities between two snapshots.
func DiffSnapshots(before, after Snapshot) Diff {
	diff := Diff{
		CategoryDelta: deltaCategories(before, after),
		StateDelta:    deltaStates(before, after),
		DeadlockDelta: len(after.Deadlocks) - len(before.Deadlocks),
	}
	beforeByID := indexThreads(before.Threads)
	afterByID := indexThreads(after.Threads)
	for key, thread := range afterByID {
		prev, ok := beforeByID[key]
		if !ok {
			diff.Added = append(diff.Added, thread)
			continue
		}
		if prev.State != thread.State || prev.Category != thread.Category || prev.TopFrame != thread.TopFrame {
			diff.StateChanged = append(diff.StateChanged, Change{
				Name:        thread.Name,
				RuntimeID:   thread.RuntimeID,
				NativeID:    thread.NativeID,
				BeforeState: prev.State,
				AfterState:  thread.State,
				BeforeCat:   prev.Category,
				AfterCat:    thread.Category,
				BeforeTop:   prev.TopFrame,
				AfterTop:    thread.TopFrame,
			})
		}
	}
	for key, thread := range beforeByID {
		if _, ok := afterByID[key]; !ok {
			diff.Removed = append(diff.Removed, thread)
		}
	}
	sortThreads(diff.Added)
	sortThreads(diff.Removed)
	sort.Slice(diff.StateChanged, func(i, j int) bool {
		return diff.StateChanged[i].Name < diff.StateChanged[j].Name
	})
	return diff
}

// BuildTimeline converts repeated parsed snapshots into aggregate timeline points.
func BuildTimeline(snapshots []Snapshot) Timeline {
	points := make([]TimelinePoint, 0, len(snapshots))
	for _, snapshot := range snapshots {
		summary := Summarize(snapshot)
		points = append(points, TimelinePoint{
			CapturedAt: snapshot.CapturedAt,
			Total:      summary.Total,
			ByCategory: summary.ByCategory,
			ByState:    summary.ByState,
			Virtual:    summary.Virtual,
			Deadlocks:  summary.Deadlocks,
		})
	}
	return Timeline{Points: points}
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func hasState(states []State, state State) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

func hasCategory(categories []WaitCategory, category WaitCategory) bool {
	for _, candidate := range categories {
		if candidate == category {
			return true
		}
	}
	return false
}

func indexThreads(threads []Thread) map[string]Thread {
	index := make(map[string]Thread, len(threads))
	for _, thread := range threads {
		index[threadKey(thread)] = thread
	}
	return index
}

func threadKey(thread Thread) string {
	if thread.NativeID != "" {
		return "native:" + thread.NativeID
	}
	if thread.RuntimeID != "" {
		return "runtime:" + thread.RuntimeID
	}
	return "name:" + thread.Name
}

func deltaCategories(before, after Snapshot) map[WaitCategory]int {
	out := make(map[WaitCategory]int)
	for category, count := range Summarize(after).ByCategory {
		out[category] += count
	}
	for category, count := range Summarize(before).ByCategory {
		out[category] -= count
	}
	return out
}

func deltaStates(before, after Snapshot) map[State]int {
	out := make(map[State]int)
	for state, count := range Summarize(after).ByState {
		out[state] += count
	}
	for state, count := range Summarize(before).ByState {
		out[state] -= count
	}
	return out
}

func sortThreads(threads []Thread) {
	sort.Slice(threads, func(i, j int) bool {
		return threadKey(threads[i]) < threadKey(threads[j])
	})
}

// RuntimeIDFromInt formats numeric runtime thread IDs for adapters that expose
// integer IDs.
func RuntimeIDFromInt(id int) string {
	if id == 0 {
		return ""
	}
	return strconv.Itoa(id)
}
