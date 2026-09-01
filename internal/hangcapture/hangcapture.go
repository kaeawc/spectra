// Package hangcapture analyzes a native `sample` report to decide whether a
// process's main thread is genuinely hung — and why (a lock, synchronous I/O,
// or a CPU spin) — or merely idle waiting for events. It reads only.
package hangcapture

import (
	"regexp"
	"strings"
)

// Verdict is the classification of a main thread's state.
type Verdict string

const (
	VerdictIdle        Verdict = "idle"
	VerdictLockBlocked Verdict = "lock-blocked"
	VerdictIOBlocked   Verdict = "io-blocked"
	VerdictSpinning    Verdict = "spinning"
	VerdictUnknown     Verdict = "unknown"
)

// MainThread is the analysis of a process's main thread.
type MainThread struct {
	Found     bool     `json:"found"`
	Verdict   Verdict  `json:"verdict"`
	Reason    string   `json:"reason"`
	Leaf      string   `json:"leaf,omitempty"`
	TopFrames []string `json:"top_frames,omitempty"`
}

// Analysis is the whole hang analysis of a sample.
type Analysis struct {
	MainThread MainThread `json:"main_thread"`
}

var frameLine = regexp.MustCompile(`^(\s+)(\d+)\s+(.*\S)\s*$`)

// lockWaits are leaf symbols that mean the thread is blocked on a lock.
var lockWaits = map[string]bool{
	"__psynch_mutexwait": true, "__psynch_cvwait": true,
	"__ulock_wait": true, "__ulock_wait2": true,
	"pthread_mutex_lock": true, "pthread_cond_wait": true,
	"pthread_rwlock_wrlock": true, "pthread_rwlock_rdlock": true,
	"os_unfair_lock_lock_slow": true,
}

// ioWaits are leaf symbols that mean the thread is blocked in a syscall.
var ioWaits = map[string]bool{
	"read": true, "__read_nocancel": true, "write": true, "__write_nocancel": true,
	"__semwait_signal": true, "select": true, "poll": true, "__select": true,
	"open": true, "__open_nocancel": true, "stat64": true, "fstat64": true, "lstat64": true,
	"connect": true, "__connect_nocancel": true, "recvfrom": true, "recvmsg": true,
	"sendto": true, "fcntl": true, "__getdirentries64": true, "fsync": true, "flock": true,
	"waitpid": true, "__wait4": true, "kevent": true, "kevent_id": true, "kevent_qos": true,
}

// machMsg leaf symbols mean the thread is parked in the Mach messaging layer;
// under a CFRunLoop that is a healthy idle wait for events.
var machMsg = map[string]bool{
	"mach_msg_trap": true, "mach_msg2_trap": true, "mach_msg": true, "mach_msg2_internal": true,
}

// Analyze classifies the main thread found in a `sample` call graph.
func Analyze(sampleOutput string) Analysis {
	chain, ok := mainThreadChain(sampleOutput)
	if !ok {
		return Analysis{MainThread: MainThread{Found: false, Verdict: VerdictUnknown, Reason: "no main thread (com.apple.main-thread) found in the sample"}}
	}
	leaf := ""
	if len(chain) > 0 {
		leaf = chain[len(chain)-1]
	}
	verdict, reason := classify(leaf, chain)
	return Analysis{MainThread: MainThread{
		Found:     true,
		Verdict:   verdict,
		Reason:    reason,
		Leaf:      leaf,
		TopFrames: topFrames(chain, 8),
	}}
}

// classify decides the verdict from the leaf frame and the thread's stack.
func classify(leaf string, chain []string) (Verdict, string) {
	switch {
	case lockWaits[leaf]:
		return VerdictLockBlocked, "main thread is blocked acquiring a lock (" + leaf + ") — it should never wait on a lock"
	case machMsg[leaf] && stackHasRunLoop(chain):
		return VerdictIdle, "main thread is idle in its run loop waiting for events (" + leaf + ")"
	case machMsg[leaf]:
		return VerdictLockBlocked, "main thread is parked in mach_msg outside the run loop (" + leaf + ") — likely a synchronous IPC/wait"
	case ioWaits[leaf]:
		return VerdictIOBlocked, "main thread is blocked in a synchronous syscall (" + leaf + ")"
	case leaf == "":
		return VerdictUnknown, "main thread stack could not be read"
	default:
		return VerdictSpinning, "main thread is on-CPU in application code (" + leaf + ") — a compute hang"
	}
}

func stackHasRunLoop(chain []string) bool {
	for _, f := range chain {
		if strings.Contains(f, "CFRunLoop") || strings.Contains(f, "__CFRunLoopServiceMachPort") {
			return true
		}
	}
	return false
}

// mainThreadChain returns the root→leaf frame chain of the main thread. It
// follows the heaviest child at each level so a branching thread still yields
// its hottest path.
func mainThreadChain(out string) ([]string, bool) {
	lines := strings.Split(out, "\n")
	start, headerIndent := -1, 0
	for i, line := range lines {
		if !strings.Contains(line, "main-thread") {
			continue
		}
		if m := frameLine.FindStringSubmatch(line); m != nil {
			start, headerIndent = i, len(m[1])
			break
		}
	}
	if start < 0 {
		return nil, false
	}

	chain := []string{cleanSymbol(lineText(lines[start]))}
	// Walk deeper frames, at each depth taking the highest-count child.
	curIndent := headerIndent
	for i := start + 1; i < len(lines); i++ {
		m := frameLine.FindStringSubmatch(lines[i])
		if m == nil {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			break // left the call graph
		}
		indent := len(m[1])
		if indent <= headerIndent {
			break // next thread
		}
		if indent <= curIndent {
			continue // sibling or shallower; keep the first (heaviest) chosen path
		}
		// First child one level deeper: take it and descend.
		if indent == curIndent+2 || indent > curIndent {
			chain = append(chain, cleanSymbol(m[3]))
			curIndent = indent
		}
	}
	return chain, true
}

func lineText(line string) string {
	if m := frameLine.FindStringSubmatch(line); m != nil {
		return m[3]
	}
	return strings.TrimSpace(line)
}

func topFrames(chain []string, n int) []string {
	// The most relevant frames are the deepest (leaf-most).
	if len(chain) <= n {
		return chain
	}
	return chain[len(chain)-n:]
}

// cleanSymbol reduces a call-graph frame's text to a bare symbol name.
func cleanSymbol(s string) string {
	if i := strings.Index(s, "  (in "); i >= 0 {
		s = s[:i]
	} else if i := strings.Index(s, " (in "); i >= 0 {
		s = s[:i]
	} else if i := strings.Index(s, "   "); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "  ["); i >= 0 {
		s = s[:i]
	}
	s = offsetTail.ReplaceAllString(s, "")
	return sanitize(strings.TrimSpace(s))
}

var offsetTail = regexp.MustCompile(`\s+\+\s+\d+$`)

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, s)
}
