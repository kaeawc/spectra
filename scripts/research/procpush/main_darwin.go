// Command procpush measures the macOS process-observation primitives compared
// in docs/research/process-data-push.md: the cost of a kern.proc.all sysctl
// against a ps exec, which pids a non-root process may watch with kqueue
// EVFILT_PROC, and how many short-lived children a fork-triggered rescan
// catches compared with fixed-interval polling.
//
// It is a research probe, not part of Spectra; it lives in its own module so
// it never ships in a Spectra build.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	children := flag.Int("children", 200, "short-lived children the fork workload spawns")
	costOnly := flag.Bool("cost-only", false, "only compare kern.proc.all with ps (for runs under load)")
	flag.Parse()
	fmt.Printf("uid=%d euid=%d\n", os.Getuid(), os.Geteuid())
	measureSysctl(200)
	measurePS(20, "-axwwo", "pid=,ppid=,pcpu=,rss=,vsz=,uid=,user=,lstart=,command=")
	measurePS(20, "-Ao", "pid=,ppid=,rss=,%cpu=,comm=")
	measurePidinfo()
	if *costOnly {
		return
	}
	measureKqueueAttach()
	measureForkCapture(*children)
}

type stats struct{ p50, p95, mean time.Duration }

func summarize(d []time.Duration) stats {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	var sum time.Duration
	for _, v := range d {
		sum += v
	}
	return stats{p50: d[len(d)/2], p95: d[len(d)*95/100], mean: sum / time.Duration(len(d))}
}

func cpu(who int) time.Duration {
	var r syscall.Rusage
	_ = syscall.Getrusage(who, &r)
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}

func measureSysctl(n int) {
	var d []time.Duration
	var procs int
	c0 := cpu(syscall.RUSAGE_SELF)
	for range n {
		t := time.Now()
		rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
		if err != nil {
			fmt.Println("kern.proc.all error:", err)
			return
		}
		d = append(d, time.Since(t))
		procs = len(rows)
	}
	s := summarize(d)
	fmt.Printf("sysctl kern.proc.all: procs=%d n=%d wall p50=%v p95=%v mean=%v cpu/call=%v\n",
		procs, n, s.p50, s.p95, s.mean, (cpu(syscall.RUSAGE_SELF)-c0)/time.Duration(n))
}

func measurePS(n int, args ...string) {
	var d []time.Duration
	c0 := cpu(syscall.RUSAGE_CHILDREN)
	for range n {
		t := time.Now()
		cmd := exec.Command("ps", args...)
		cmd.Env = append(os.Environ(), "TZ=UTC0", "LC_ALL=C")
		if _, err := cmd.Output(); err != nil {
			fmt.Println("ps error:", err)
			return
		}
		d = append(d, time.Since(t))
	}
	s := summarize(d)
	fmt.Printf("exec ps %v: n=%d wall p50=%v p95=%v mean=%v child-cpu/call=%v\n",
		args, n, s.p50, s.p95, s.mean, (cpu(syscall.RUSAGE_CHILDREN)-c0)/time.Duration(n))
}

// measureKqueueAttach registers EVFILT_PROC on every visible pid, grouped by
// whether the target is ours, root's, or another user's, first without and
// then with NOTE_EXITSTATUS (the only flag XNU permission-checks).
func measureKqueueAttach() {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		fmt.Println("kern.proc.all error:", err)
		return
	}
	me := uint32(os.Getuid())
	for _, mode := range []struct {
		name   string
		fflags uint32
	}{
		{"NOTE_EXIT|NOTE_FORK|NOTE_EXEC", unix.NOTE_EXIT | unix.NOTE_FORK | unix.NOTE_EXEC},
		{"NOTE_EXIT|NOTE_EXITSTATUS", unix.NOTE_EXIT | unix.NOTE_EXITSTATUS},
	} {
		kq, err := unix.Kqueue()
		if err != nil {
			fmt.Println("kqueue error:", err)
			return
		}
		res := map[string]map[string]int{}
		t := time.Now()
		for _, r := range rows {
			pid := int(r.Proc.P_pid)
			if pid == 0 {
				continue
			}
			owner := "other"
			switch uid := r.Eproc.Pcred.P_ruid; {
			case uid == me:
				owner = "self"
			case uid == 0:
				owner = "root"
			}
			ev := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: mode.fflags}
			_, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil)
			outcome := "ok"
			if err != nil {
				outcome = err.Error()
			}
			if res[owner] == nil {
				res[owner] = map[string]int{}
			}
			res[owner][outcome]++
		}
		elapsed := time.Since(t)
		_ = unix.Close(kq)
		fmt.Printf("kqueue EVFILT_PROC %s: %d pids in %v (%v/pid) results=%v\n",
			mode.name, len(rows), elapsed, elapsed/time.Duration(len(rows)), res)
	}
}

// measureForkCapture runs a shell that spawns short-lived /usr/bin/true
// children and counts how many each strategy observes: a kern.proc.all
// rescan triggered by NOTE_FORK on the shell, and 250 ms polling with
// kern.proc.all (the spectra spawns default interval).
func measureForkCapture(n int) {
	script := fmt.Sprintf("read _; i=0; while [ $i -lt %d ]; do /usr/bin/true; i=$((i+1)); done", n)
	cmd := exec.Command("/bin/sh", "-c", script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Println("pipe error:", err)
		return
	}
	if err := cmd.Start(); err != nil {
		fmt.Println("start error:", err)
		return
	}
	sh := cmd.Process.Pid
	kq, err := unix.Kqueue()
	if err != nil {
		fmt.Println("kqueue error:", err)
		return
	}
	defer unix.Close(kq)
	ev := unix.Kevent_t{Ident: uint64(sh), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: unix.NOTE_EXIT | unix.NOTE_FORK | unix.NOTE_EXEC}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		fmt.Println("kevent register error:", err)
		return
	}

	var mu sync.Mutex
	polled := map[int32]bool{}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			rows, _ := unix.SysctlKinfoProcSlice("kern.proc.all")
			mu.Lock()
			for _, r := range rows {
				if r.Eproc.Ppid == int32(sh) {
					polled[r.Proc.P_pid] = true
				}
			}
			mu.Unlock()
		}
	}()

	_, _ = stdin.Write([]byte("\n"))
	seen := map[int32]bool{}
	var forkEvents, rescans int
	var rescanTime time.Duration
	start := time.Now()
	c0 := cpu(syscall.RUSAGE_SELF)
	events := make([]unix.Kevent_t, 16)
	exited := false
	for !exited {
		k, err := unix.Kevent(kq, nil, events, &unix.Timespec{Sec: 10})
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || k == 0 {
			fmt.Println("kevent wait ended:", err)
			break
		}
		for _, e := range events[:k] {
			if e.Fflags&unix.NOTE_EXIT != 0 {
				exited = true
			}
			if e.Fflags&unix.NOTE_FORK == 0 {
				continue
			}
			forkEvents++
			t := time.Now()
			rows, _ := unix.SysctlKinfoProcSlice("kern.proc.all")
			rescanTime += time.Since(t)
			rescans++
			for _, r := range rows {
				if r.Eproc.Ppid == int32(sh) {
					seen[r.Proc.P_pid] = true
				}
			}
		}
	}
	elapsed := time.Since(start)
	selfCPU := cpu(syscall.RUSAGE_SELF) - c0
	cancel()
	wg.Wait()
	_ = cmd.Wait()
	mu.Lock()
	defer mu.Unlock()
	fmt.Printf("fork capture: children=%d run=%v fork-events=%d rescans=%d rescan-total=%v "+
		"caught-by-fork-rescan=%d (%.0f%%) caught-by-250ms-poll=%d (%.0f%%) observer-cpu=%v\n",
		n, elapsed, forkEvents, rescans, rescanTime,
		len(seen), 100*float64(len(seen))/float64(n), len(polled), 100*float64(len(polled))/float64(n), selfCPU)
}
