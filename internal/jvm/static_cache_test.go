package jvm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type staticFake struct {
	mu      sync.Mutex
	counts  map[string]int
	start   string
	threads string
}

func (f *staticFake) run(name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "ps" {
		if f.start == "" {
			return nil, errors.New("ps failed")
		}
		return []byte("7 " + f.start + "\n"), nil
	}
	if name == "jcmd" && len(args) >= 2 {
		f.counts[args[1]]++
		switch args[1] {
		case "VM.version":
			return []byte("7:\nJDK 21\n"), nil
		case "VM.system_properties":
			return []byte("java.home=/jdk\njava.version=21\n"), nil
		case "VM.command_line":
			return []byte("VM Arguments:\njvm_args: -Xmx1g\n"), nil
		case "Thread.print":
			return []byte(f.threads), nil
		}
	}
	return nil, errors.New("unsupported")
}

func threadDumpOf(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("\"T" + string(rune('a'+i)) + "\" #1 prio=5 os_prio=0 tid=0x1 nid=0x1 runnable\n   java.lang.Thread.State: RUNNABLE\n\n")
	}
	return b.String()
}

func TestStaticFactsCachedByPidAndStart(t *testing.T) {
	f := &staticFake{counts: map[string]int{}, start: "Sat Nov 21 10:00:00 2026", threads: threadDumpOf(2)}
	opts := CollectOptions{CmdRunner: f.run, StaticCache: NewStaticCache()}

	first := InspectPID(context.Background(), 7, opts)
	f.threads = threadDumpOf(3)
	second := InspectPID(context.Background(), 7, opts)
	if first == nil || second == nil {
		t.Fatal("nil info")
	}
	if f.counts["VM.system_properties"] != 1 || f.counts["VM.command_line"] != 1 {
		t.Fatalf("static jcmd re-run: %v", f.counts)
	}
	if second.JavaHome != "/jdk" || second.VMArgs != "-Xmx1g" {
		t.Fatalf("cached facts wrong: %+v", second)
	}
	if first.ThreadCount != 2 || second.ThreadCount != 3 || f.counts["Thread.print"] != 2 {
		t.Fatalf("thread count not live: %d %d %v", first.ThreadCount, second.ThreadCount, f.counts)
	}

	f.start = "Sat Nov 21 11:00:00 2026" // pid reuse
	InspectPID(context.Background(), 7, opts)
	if f.counts["VM.system_properties"] != 2 || f.counts["VM.command_line"] != 2 {
		t.Fatalf("pid reuse did not re-run: %v", f.counts)
	}
}

func TestStaticCachePruneAndCap(t *testing.T) {
	c := NewStaticCache()
	c.put(staticKey{1, "a"}, staticFacts{})
	c.put(staticKey{2, "b"}, staticFacts{})
	c.Prune(map[int]string{1: "a", 2: "other"})
	if c.Len() != 1 {
		t.Fatalf("len=%d", c.Len())
	}
	for i := 0; i < maxStaticCacheEntries+50; i++ {
		c.put(staticKey{i + 10, "x"}, staticFacts{})
	}
	if c.Len() > maxStaticCacheEntries {
		t.Fatalf("cap exceeded: %d", c.Len())
	}
}

func TestStaticNoStartTimeNotCached(t *testing.T) {
	f := &staticFake{counts: map[string]int{}, start: "", threads: threadDumpOf(1)}
	opts := CollectOptions{CmdRunner: f.run, StaticCache: NewStaticCache()}
	InspectPID(context.Background(), 7, opts)
	InspectPID(context.Background(), 7, opts)
	if f.counts["VM.system_properties"] != 2 {
		t.Fatalf("cached without start time: %v", f.counts)
	}
}

func TestStaticFactsCachedWithoutJVMArgs(t *testing.T) {
	cache := NewStaticCache()
	var mu sync.Mutex
	cmdline := 0
	run := func(name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if name == "jcmd" && len(args) >= 2 {
			switch args[1] {
			case "VM.system_properties":
				return []byte("java.home=/jdk\n"), nil
			case "VM.command_line":
				cmdline++
				return []byte("VM Arguments:\njava_command: Main\n"), nil // no jvm_args line
			}
		}
		return nil, errors.New("unsupported")
	}
	collectStatic(7, "start", run, cache)
	collectStatic(7, "start", run, cache)
	if cmdline != 1 {
		t.Fatalf("JVM without options re-probed: %d command_line calls", cmdline)
	}
}

func TestProcessStartTimesFallsBackPerPid(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) ([]byte, error) {
		list := args[len(args)-1]
		calls = append(calls, list)
		switch list {
		case "7":
			return []byte("7 Sat Nov 21 10:00:00 2026\n"), nil
		case "9":
			return nil, errors.New("exit 1")
		}
		return nil, errors.New("exit 1") // batch fails because pid 9 exited
	}
	got := processStartTimes(run, []int{7, 9})
	if got[7] != "Sat Nov 21 10:00:00 2026" || len(got) != 1 {
		t.Fatalf("start times = %v (calls %v)", got, calls)
	}
}

func TestPruneSkippedWhenStartTimesUnknown(t *testing.T) {
	cache := NewStaticCache()
	cache.put(staticKey{7, "s"}, staticFacts{haveProp: true, haveArgs: true})
	run := func(name string, args ...string) ([]byte, error) {
		if name == "jps" {
			return []byte("7 Main\n"), nil
		}
		return nil, errors.New("fail")
	}
	CollectAllStatus(context.Background(), CollectOptions{CmdRunner: run, StaticCache: cache})
	if _, ok := cache.get(staticKey{7, "s"}); !ok {
		t.Fatal("cache wiped although start times were unknown")
	}
}
