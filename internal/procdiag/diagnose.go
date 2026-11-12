package procdiag

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/pressure"
)

// Thresholds. They are deliberately conservative: a false "your process is
// deadlocked" costs the reader more than a missed borderline case.
const (
	cpuSpinPct           = 90.0 // of one core, over the interval
	fdWarnPct            = 80.0 // of RLIMIT_NOFILE
	fdCritPct            = 95.0 //
	fdGrowthPerSec       = 5.0  // sustained, on top of a minimum absolute delta
	fdGrowthMinDelta     = 5    //
	memGrowthKiBPerSec   = 8192 // 8 MiB/s
	swapMinKiB           = 262144
	swapShareOfFootprint = 0.25
)

// idleSyscalls are where a healthy, waiting thread parks. A main thread in
// one of these is not "blocked" in any problematic sense.
var idleSyscalls = map[string]bool{
	"epoll_wait": true, "epoll_pwait": true, "epoll_pwait2": true,
	"poll": true, "ppoll": true, "select": true, "pselect6": true,
	"nanosleep": true, "clock_nanosleep": true, "pause": true,
	"accept": true, "accept4": true, "wait4": true, "waitid": true,
	"rt_sigtimedwait": true, "rt_sigsuspend": true, "io_uring_enter": true,
	"io_getevents": true, "futex_waitv": true,
}

// networkSyscalls hint that the thread is waiting on a peer.
var networkSyscalls = map[string]bool{
	"connect": true, "recvfrom": true, "recvmsg": true, "recvmmsg": true,
	"sendto": true, "sendmsg": true, "sendmmsg": true,
}

// storageSyscalls are waits that normally mean a slow disk, NFS server, or
// FUSE daemon.
var storageSyscalls = map[string]bool{
	"fsync": true, "fdatasync": true, "sync": true, "syncfs": true, "msync": true,
	"flock": true, "fallocate": true, "ftruncate": true,
}

func diagnose(r *Report, a, b procSample, totalSysTicks int64) []Finding {
	var f []Finding
	switch r.State {
	case "Z":
		f = append(f, Finding{
			Kind: KindZombie, Severity: SeverityWarning,
			Summary:  "process has exited but its parent has not reaped it (wait()); it holds a pid slot but no memory",
			Evidence: []string{"state=Z"},
		})
		return f // nothing else about a zombie is meaningful
	case "T", "t":
		f = append(f, Finding{
			Kind: KindStopped, Severity: SeverityWarning,
			Summary:  "process is stopped (SIGSTOP/SIGTSTP or ptrace-attached); it will make no progress until continued",
			Evidence: []string{"state=" + r.State, "send SIGCONT or detach the debugger"},
		})
	}
	f = append(f, diagnoseThreads(r, a, b, totalSysTicks)...)
	f = append(f, diagnoseFDs(r)...)
	f = append(f, diagnoseMemory(r)...)
	if r.Cgroup != nil {
		for _, pf := range pressure.AssessCgroup(*r.Cgroup) {
			f = append(f, Finding{Kind: pf.Kind, Severity: Severity(pf.Severity), Summary: pf.Summary})
		}
	}
	sort.SliceStable(f, func(i, j int) bool { return rank(f[i].Severity) > rank(f[j].Severity) })
	return f
}

func rank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	default:
		return 1
	}
}

func diagnoseThreads(r *Report, a, b procSample, totalSysTicks int64) []Finding {
	var f []Finding
	f = append(f, uninterruptible(r, a, b)...)
	if x := lockWait(r, a, b); x != nil {
		f = append(f, *x)
	}
	if x := blockedMain(r, a, b); x != nil {
		f = append(f, *x)
	}
	if r.CPUPct >= cpuSpinPct {
		f = append(f, cpuSpin(r, totalSysTicks))
	}
	return f
}

// uninterruptible reports threads in D state in BOTH samples. A single D
// reading is ordinary disk wait; D across the whole interval means the
// thread cannot be killed or interrupted, which is almost always a hung
// block device, NFS/FUSE mount, or a kernel bug.
func uninterruptible(r *Report, a, b procSample) []Finding {
	var stuck []string
	for tid, t := range b.tasks {
		if p, ok := a.tasks[tid]; ok && t.state == "D" && p.state == "D" {
			w := t.wchan
			if w == "" {
				w = "?"
			}
			stuck = append(stuck, fmt.Sprintf("tid %d (%s) wchan=%s", tid, t.comm, w))
		}
	}
	if len(stuck) == 0 {
		return nil
	}
	sort.Strings(stuck)
	return []Finding{{
		Kind: KindUninterrupt, Severity: SeverityCritical,
		Summary:  fmt.Sprintf("%d thread(s) stayed in uninterruptible sleep (D) for the whole %.0fs interval — SIGKILL will not work; suspect a hung disk, NFS/FUSE mount, or driver", len(stuck), r.IntervalSec),
		Evidence: stuck,
	}}
}

func isFutex(t taskSample, arch string) bool {
	if t.syscallNr >= 0 && SyscallName(arch, t.syscallNr) == "futex" {
		return true
	}
	return strings.HasPrefix(t.wchan, "futex")
}

// lockWait flags a multi-threaded process in which every thread is parked on
// a futex and none was scheduled at all during the interval. A healthy idle
// server parks in epoll_wait, and a healthy worker pool wakes periodically,
// so "all futex and zero context switches" is a strong (not conclusive)
// deadlock or lost-wakeup signal.
func lockWait(r *Report, a, b procSample) *Finding {
	if len(b.tasks) < 2 {
		return nil
	}
	var evidence []string
	for tid, t := range b.tasks {
		p, ok := a.tasks[tid]
		if !ok || !isFutex(t, r.arch) || !isFutex(p, r.arch) {
			return nil
		}
		if t.state != "S" || t.ctx != p.ctx {
			return nil
		}
		evidence = append(evidence, fmt.Sprintf("tid %d (%s) in futex", tid, t.comm))
	}
	sort.Strings(evidence)
	return &Finding{
		Kind: KindLockWait, Severity: SeverityWarning,
		Summary:  fmt.Sprintf("all %d threads were parked on a futex and none ran for %.0fs — consistent with a deadlock or lost wake-up (also what an idle thread pool looks like; confirm with a stack dump)", len(b.tasks), r.IntervalSec),
		Evidence: evidence,
	}
}

func blockedMain(r *Report, a, b procSample) *Finding {
	m, ok := b.tasks[r.PID]
	p, okPrev := a.tasks[r.PID]
	if !ok || !okPrev || m.state != "S" || p.state != "S" || m.syscallNr < 0 || m.syscallNr != p.syscallNr || m.ctx != p.ctx {
		return nil
	}
	name := SyscallName(r.arch, m.syscallNr)
	if idleSyscalls[name] {
		return nil
	}
	sev, why := SeverityInfo, "may be normal for a program waiting on input"
	switch {
	case networkSyscalls[name]:
		sev, why = SeverityWarning, "it is waiting on a network peer"
	case storageSyscalls[name]:
		sev, why = SeverityWarning, "it is waiting on storage or a lock; check disk, NFS and FUSE health"
	}
	if name == "futex" {
		return nil // lock waits are judged across all threads by lockWait
	}
	ev := []string{fmt.Sprintf("syscall=%s", name)}
	if m.wchan != "" {
		ev = append(ev, "wchan="+m.wchan)
	}
	return &Finding{
		Kind: KindBlockedSys, Severity: sev,
		Summary:  fmt.Sprintf("main thread sat in %s() for the whole %.0fs interval without being scheduled; %s", name, r.IntervalSec, why),
		Evidence: ev,
	}
}

func cpuSpin(r *Report, totalSysTicks int64) Finding {
	ev := []string{fmt.Sprintf("process cpu=%.0f%% of one core over %.1fs", r.CPUPct, r.IntervalSec)}
	if len(r.Threads) > 0 {
		t := r.Threads[0]
		ev = append(ev, fmt.Sprintf("hottest thread tid %d (%s) at %.0f%%", t.TID, t.Comm, t.CPUPct))
	}
	hot := "user-space loop"
	if r.CPUPct > 0 {
		sysShare := 100 * float64(totalSysTicks) / clkTck / r.IntervalSec / r.CPUPct * 100
		ev = append(ev, fmt.Sprintf("%.0f%% of that time in the kernel", sysShare))
		if sysShare >= 50 {
			hot = "syscall storm (most time is in the kernel; try strace -c -p)"
		}
	}
	return Finding{
		Kind: KindCPUSpin, Severity: SeverityWarning,
		Summary:  "process is pegging CPU — " + hot,
		Evidence: ev,
	}
}

func diagnoseFDs(r *Report) []Finding {
	fd := r.FDs
	var f []Finding
	if fd.Limit > 0 {
		used := 100 * float64(fd.Count) / float64(fd.Limit)
		if sev := fdSeverity(used); sev != "" {
			f = append(f, Finding{
				Kind: KindFDExhaustion, Severity: sev,
				Summary:  fmt.Sprintf("%d of %d file descriptors in use (%.0f%%); the next open()/accept()/socket() will fail with EMFILE once the limit is hit", fd.Count, fd.Limit, used),
				Evidence: []string{fmt.Sprintf("RLIMIT_NOFILE soft=%d", fd.Limit)},
			})
		}
	}
	if fd.Delta >= fdGrowthMinDelta && fd.RatePerSec >= fdGrowthPerSec {
		ev := []string{fmt.Sprintf("+%d fds in %.1fs (%.1f/s)", fd.Delta, r.IntervalSec, fd.RatePerSec)}
		eta := ""
		if fd.Limit > 0 {
			secs := float64(fd.Limit-int64(fd.Count)) / fd.RatePerSec
			eta = fmt.Sprintf("; at this rate the limit is reached in %s", humanDuration(secs))
			ev = append(ev, fmt.Sprintf("limit=%d", fd.Limit))
		}
		f = append(f, Finding{
			Kind: KindFDGrowth, Severity: SeverityWarning,
			Summary:  fmt.Sprintf("file descriptor count is climbing %.1f/s — likely a leak%s (ls -l /proc/%d/fd shows what)", fd.RatePerSec, eta, r.PID),
			Evidence: ev,
		})
	}
	return f
}

func fdSeverity(usedPct float64) Severity {
	switch {
	case usedPct >= fdCritPct:
		return SeverityCritical
	case usedPct >= fdWarnPct:
		return SeverityWarning
	default:
		return ""
	}
}

func diagnoseMemory(r *Report) []Finding {
	m := r.Memory
	var f []Finding
	if m.RSSRateKiBPS >= memGrowthKiBPerSec {
		f = append(f, Finding{
			Kind: KindMemoryGrowth, Severity: SeverityWarning,
			Summary:  fmt.Sprintf("resident memory grew %.1f MiB/s (+%d MiB in %.1fs); sample again over a longer interval to separate a leak from a warm-up", m.RSSRateKiBPS/1024, m.RSSDeltaKiB/1024, r.IntervalSec),
			Evidence: []string{fmt.Sprintf("rss=%d MiB", m.RSSKiB/1024)},
		})
	}
	if footprint := m.RSSKiB + m.SwapKiB; m.SwapKiB >= swapMinKiB && footprint > 0 && float64(m.SwapKiB)/float64(footprint) >= swapShareOfFootprint {
		f = append(f, Finding{
			Kind: KindSwappedOut, Severity: SeverityInfo,
			Summary:  fmt.Sprintf("%d MiB of this process lives in swap (%.0f%% of its footprint); page faults back in will look like latency", m.SwapKiB/1024, 100*float64(m.SwapKiB)/float64(footprint)),
			Evidence: []string{fmt.Sprintf("rss=%d MiB swap=%d MiB", m.RSSKiB/1024, m.SwapKiB/1024)},
		})
	}
	return f
}

func humanDuration(secs float64) string {
	switch {
	case secs < 90:
		return fmt.Sprintf("%.0fs", secs)
	case secs < 5400:
		return fmt.Sprintf("%.0fm", secs/60)
	default:
		return fmt.Sprintf("%.1fh", secs/3600)
	}
}

// verdict picks the headline: the kind of the most severe finding, else
// "active" or "idle" by CPU use.
func verdict(f []Finding, cpuPct float64) string {
	if len(f) > 0 && rank(f[0].Severity) >= rank(SeverityWarning) {
		return f[0].Kind
	}
	if cpuPct >= 5 {
		return KindActive
	}
	return KindIdle
}
