package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/threadinspect"
)

// ruleJVMDeadlock fires for each Java-level lock cycle found in a thread dump
// captured for a targeted JVM (snapshot.Options.ThreadDumpPIDs). High
// severity: deadlocked threads never recover without a restart.
func ruleJVMDeadlock() Rule {
	return Rule{
		ID:       "jvm-deadlock",
		Severity: SeverityHigh,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, r := range s.JVMDeadlocks {
				for _, c := range r.Cycles {
					findings = append(findings, Finding{
						RuleID:   "jvm-deadlock",
						Severity: SeverityHigh,
						Subject:  deadlockSubject(r, c),
						Message:  deadlockMessage(c),
						Fix: fmt.Sprintf("Capture `spectra jvm thread-dump %d` for the full stacks, then acquire these locks in one "+
							"consistent order (or use tryLock with a timeout); the deadlocked threads will not recover without a restart.", r.PID),
					})
				}
			}
			return findings
		},
	}
}

// deadlockSubject keys the finding on the PID plus the sorted thread set so
// the issue lifecycle tracks each distinct cycle separately and stably.
func deadlockSubject(r snapshot.JVMDeadlockReport, c threadinspect.DeadlockCycle) string {
	threads := append([]string(nil), c.Threads...)
	sort.Strings(threads)
	subject := fmt.Sprintf("PID %d", r.PID)
	if r.MainClass != "" {
		subject += fmt.Sprintf(" (%s)", r.MainClass)
	}
	return subject + " deadlock: " + strings.Join(threads, ", ")
}

func deadlockMessage(c threadinspect.DeadlockCycle) string {
	head := fmt.Sprintf("Java-level deadlock between %d threads: ", len(c.Threads))
	if len(c.Waits) == 0 {
		msg := head + quoteJoin(c.Threads, " -> ")
		if len(c.Locks) > 0 {
			msg += " (locks: " + strings.Join(c.Locks, ", ") + ")"
		}
		return msg + "."
	}
	edges := make([]string, 0, len(c.Waits))
	for _, w := range c.Waits {
		edge := fmt.Sprintf("%q waits for", w.Thread)
		if w.Lock != "" {
			edge += " " + w.Lock
		} else {
			edge += " a lock"
		}
		if w.HeldBy != "" {
			edge += fmt.Sprintf(" held by %q", w.HeldBy)
		}
		edges = append(edges, edge)
	}
	return head + strings.Join(edges, "; ") + "."
}

func quoteJoin(items []string, sep string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(quoted, sep)
}
