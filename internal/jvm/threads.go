package jvm

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/threadinspect"
)

type ThreadState = threadinspect.State
type WaitCategory = threadinspect.WaitCategory
type ParsedThreadDump = threadinspect.Snapshot
type Thread = threadinspect.Thread
type DeadlockCycle = threadinspect.DeadlockCycle
type DeadlockWait = threadinspect.DeadlockWait
type ThreadSummary = threadinspect.Summary
type ThreadFilter = threadinspect.Filter
type ThreadDumpDiff = threadinspect.Diff
type ThreadChange = threadinspect.Change
type ThreadTimeline = threadinspect.Timeline
type ThreadTimelinePoint = threadinspect.TimelinePoint

const (
	ThreadStateUnknown      = threadinspect.StateUnknown
	ThreadStateRunnable     = threadinspect.StateRunnable
	ThreadStateBlocked      = threadinspect.StateBlocked
	ThreadStateWaiting      = threadinspect.StateWaiting
	ThreadStateTimedWaiting = threadinspect.StateTimedWaiting
	ThreadStateTerminated   = threadinspect.StateTerminated

	WaitCategoryRunning  = threadinspect.WaitCategoryRunning
	WaitCategorySleeping = threadinspect.WaitCategorySleeping
	WaitCategoryWait     = threadinspect.WaitCategoryWait
	WaitCategoryPark     = threadinspect.WaitCategoryPark
	WaitCategoryMonitor  = threadinspect.WaitCategoryMonitor
	WaitCategoryUnknown  = threadinspect.WaitCategoryUnknown
)

// ThreadDumpParser adapts raw jcmd Thread.print bytes into the runtime-neutral
// threadinspect.Parser interface.
type ThreadDumpParser struct{}

// ParseThreads parses raw jcmd Thread.print output.
func (ThreadDumpParser) ParseThreads(raw []byte, capturedAt time.Time) (threadinspect.Snapshot, error) {
	return ParseThreadDump(string(raw), capturedAt), nil
}

// ThreadDumpCapturer adapts jcmd Thread.print into the runtime-neutral
// threadinspect.Capturer interface.
type ThreadDumpCapturer struct {
	Run CmdRunner
	Now func() time.Time
}

// CaptureThreads captures and parses a JVM thread dump for pid.
func (c ThreadDumpCapturer) CaptureThreads(ctx context.Context, pid int) (threadinspect.Snapshot, error) {
	select {
	case <-ctx.Done():
		return threadinspect.Snapshot{}, ctx.Err()
	default:
	}
	raw, err := ThreadDump(pid, c.Run)
	if err != nil {
		return threadinspect.Snapshot{}, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	dump := ParseThreadDump(string(raw), now())
	dump.PID = pid
	return dump, nil
}

var (
	_ threadinspect.Parser   = ThreadDumpParser{}
	_ threadinspect.Capturer = ThreadDumpCapturer{}

	threadHeaderRE = regexp.MustCompile(`^"([^"]*)"\s+#([0-9]+)\b(.*)$`)
	stateLineRE    = regexp.MustCompile(`^\s+java\.lang\.Thread\.State:\s+([A-Z_]+)(?:\s+\(([^)]*)\))?`)
	lockOwnerRE    = regexp.MustCompile(`owned by "([^"]+)" Id=([0-9]+)`)
)

// ParseThreadDump parses jcmd Thread.print output.
func ParseThreadDump(out string, capturedAt time.Time) ParsedThreadDump {
	var dump ParsedThreadDump
	dump.Runtime = threadinspect.RuntimeJVM
	dump.CapturedAt = capturedAt
	blocks := splitThreadBlocks(out)
	for _, block := range blocks {
		thread, ok := parseThreadBlock(block)
		if ok {
			dump.Threads = append(dump.Threads, thread)
		}
	}
	dump.Deadlocks = parseDeadlocks(out)
	return dump
}

// SummarizeThreads returns reusable aggregate counts for a parsed dump.
func SummarizeThreads(dump ParsedThreadDump) ThreadSummary {
	return threadinspect.Summarize(dump)
}

// FilterThreads returns threads matching filter.
func FilterThreads(dump ParsedThreadDump, filter ThreadFilter) []Thread {
	return threadinspect.FilterThreads(dump, filter)
}

// DiffThreadDumps compares stable thread identities between two dumps.
func DiffThreadDumps(before, after ParsedThreadDump) ThreadDumpDiff {
	return threadinspect.DiffSnapshots(before, after)
}

// BuildThreadTimeline converts repeated parsed dumps into aggregate timeline points.
func BuildThreadTimeline(dumps []ParsedThreadDump) ThreadTimeline {
	return threadinspect.BuildTimeline(dumps)
}

func splitThreadBlocks(out string) []string {
	var blocks []string
	var current []string
	for _, line := range strings.Split(out, "\n") {
		if isThreadHeader(line) {
			if len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
			}
			current = []string{line}
			continue
		}
		if len(current) > 0 {
			if strings.TrimSpace(line) == "" {
				blocks = append(blocks, strings.Join(current, "\n"))
				current = nil
				continue
			}
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	return blocks
}

func parseThreadBlock(block string) (Thread, bool) {
	lines := strings.Split(block, "\n")
	if len(lines) == 0 {
		return Thread{}, false
	}
	match := threadHeaderRE.FindStringSubmatch(lines[0])
	if match == nil {
		return Thread{}, false
	}
	javaID, _ := strconv.Atoi(match[2])
	thread := Thread{
		Name:      match[1],
		RuntimeID: threadinspect.RuntimeIDFromInt(javaID),
		RawHeader: lines[0],
		RawBlock:  block,
	}
	headerRest := match[3]
	thread.Daemon = strings.Contains(headerRest, " daemon ")
	thread.Virtual = strings.Contains(headerRest, " virtual ") || strings.Contains(headerRest, "VirtualThread")
	thread.NativeID = headerValue(headerRest, "nid=")
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if stateMatch := stateLineRE.FindStringSubmatch(line); stateMatch != nil {
			thread.State = ThreadState(stateMatch[1])
			if len(stateMatch) > 2 {
				thread.Detail = stateMatch[2]
			}
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			parseLockLine(trimmed, &thread)
			continue
		}
		if strings.HasPrefix(trimmed, "at ") && thread.TopFrame == "" {
			thread.TopFrame = strings.TrimPrefix(trimmed, "at ")
		}
		if trimmed != "" {
			thread.Stack = append(thread.Stack, trimmed)
		}
	}
	if thread.State == "" {
		thread.State = ThreadStateUnknown
	}
	thread.Category = classifyWait(thread)
	return thread, true
}

func parseLockLine(line string, thread *Thread) {
	if strings.Contains(line, "parking to wait for") || strings.Contains(line, "waiting on") || strings.Contains(line, "waiting to lock") || strings.Contains(line, "locked") {
		if start := strings.Index(line, "<"); start >= 0 {
			if end := strings.Index(line[start:], ">"); end >= 0 {
				thread.Lock = line[start : start+end+1]
			}
		}
	}
	if owner := lockOwnerRE.FindStringSubmatch(line); owner != nil {
		thread.LockOwner = owner[1]
		thread.LockOwnerID = owner[2]
	}
}

func classifyWait(thread Thread) WaitCategory {
	detail := strings.ToLower(thread.Detail)
	block := strings.ToLower(thread.RawBlock)
	switch thread.State {
	case ThreadStateRunnable:
		return WaitCategoryRunning
	case ThreadStateBlocked:
		return WaitCategoryMonitor
	case ThreadStateWaiting, ThreadStateTimedWaiting:
		if strings.Contains(detail, "sleeping") {
			return WaitCategorySleeping
		}
		if strings.Contains(detail, "parking") || strings.Contains(block, "parking to wait for") {
			return WaitCategoryPark
		}
		if strings.Contains(detail, "on object monitor") || strings.Contains(detail, "in object.wait") || strings.Contains(block, "waiting on") {
			return WaitCategoryWait
		}
		return WaitCategoryWait
	default:
		return WaitCategoryUnknown
	}
}

// HotSpot prints one "Found one Java-level deadlock:" section per cycle,
// followed by the stacks of the threads involved. Only the cycle section is
// parsed; matching "deadlock" anywhere else (thread names, frames) would
// otherwise produce phantom cycles.
const (
	deadlockSectionStart = "Found one Java-level deadlock:"
	deadlockSectionEnd   = "Java stack information for the threads listed above:"
)

var (
	monitorWaitRE = regexp.MustCompile(`waiting to lock monitor \S+ \(object (0x[0-9a-fA-F]+), a ([^)]+)\)`)
	ownableWaitRE = regexp.MustCompile(`waiting for ownable synchronizer (0x[0-9a-fA-F]+), \(a ([^)]+)\)`)
	heldByRE      = regexp.MustCompile(`which is held by "([^"]+)"`)
)

func parseDeadlocks(out string) []DeadlockCycle {
	var cycles []DeadlockCycle
	var current *DeadlockCycle
	flush := func() {
		if current != nil && len(current.Threads) > 1 {
			cycles = append(cycles, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, deadlockSectionStart):
			flush()
			current = &DeadlockCycle{}
		case current == nil:
		case strings.HasPrefix(trimmed, deadlockSectionEnd), isDeadlockTotalLine(trimmed):
			flush()
		default:
			parseDeadlockLine(current, trimmed)
		}
	}
	flush()
	return cycles
}

func isDeadlockTotalLine(line string) bool {
	return strings.HasPrefix(line, "Found ") &&
		(strings.HasSuffix(line, " deadlock.") || strings.HasSuffix(line, " deadlocks."))
}

func parseDeadlockLine(c *DeadlockCycle, line string) {
	if name, ok := parseDeadlockThreadLine(line); ok {
		c.Threads = append(c.Threads, name)
		c.Waits = append(c.Waits, DeadlockWait{Thread: name})
		return
	}
	if len(c.Waits) == 0 {
		return
	}
	w := &c.Waits[len(c.Waits)-1]
	if lock := deadlockLock(line); lock != "" && w.Lock == "" {
		w.Lock = lock
		c.Locks = append(c.Locks, lock)
		return
	}
	if m := heldByRE.FindStringSubmatch(line); m != nil && w.HeldBy == "" {
		w.HeldBy = m[1]
	}
}

// parseDeadlockThreadLine matches a cycle entry header such as `"worker-1":`.
func parseDeadlockThreadLine(line string) (string, bool) {
	if len(line) < 4 || !strings.HasPrefix(line, `"`) || !strings.HasSuffix(line, `":`) {
		return "", false
	}
	return line[1 : len(line)-2], true
}

// deadlockLock renders the awaited lock in the same `<addr> (a Class)` form
// used by stack frames so it can be correlated with the dump.
func deadlockLock(line string) string {
	for _, re := range []*regexp.Regexp{monitorWaitRE, ownableWaitRE} {
		if m := re.FindStringSubmatch(line); m != nil {
			return "<" + m[1] + "> (a " + m[2] + ")"
		}
	}
	if strings.HasPrefix(line, "waiting to lock") || strings.HasPrefix(line, "waiting for ") {
		return strings.TrimSuffix(line, ",")
	}
	return ""
}

func isThreadHeader(line string) bool {
	return threadHeaderRE.MatchString(line)
}

func headerValue(header, key string) string {
	idx := strings.Index(header, key)
	if idx < 0 {
		return ""
	}
	value := header[idx+len(key):]
	if end := strings.IndexAny(value, " \t"); end >= 0 {
		value = value[:end]
	}
	return strings.Trim(value, ",")
}
