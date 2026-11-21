package jvm

import (
	"maps"
	"strconv"
	"strings"
	"sync"
)

// maxStaticCacheEntries bounds the cache for a long-lived daemon on a host
// that churns through many short-lived JVMs.
const maxStaticCacheEntries = 512

type staticKey struct {
	pid   int
	start string
}

type staticFacts struct {
	sysProps map[string]string
	vmFlags  string
	vmArgs   string
	haveProp bool
	haveArgs bool
}

// StaticCache remembers facts that cannot change for the life of a JVM
// (VM.system_properties, VM.command_line), keyed on pid plus process start
// time so a reused pid never returns a previous process's facts. There is no
// TTL. It is safe for concurrent use.
type StaticCache struct {
	mu sync.Mutex
	m  map[staticKey]staticFacts
}

// NewStaticCache returns an empty cache.
func NewStaticCache() *StaticCache {
	return &StaticCache{m: make(map[staticKey]staticFacts)}
}

// DefaultStaticCache is shared by collections that do not set
// CollectOptions.StaticCache, so a daemon or MCP server benefits across calls.
var DefaultStaticCache = NewStaticCache()

func (c *StaticCache) get(k staticKey) (staticFacts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.m[k]
	if ok {
		f.sysProps = maps.Clone(f.sysProps)
	}
	return f, ok
}

func (c *StaticCache) put(k staticKey, f staticFacts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[k]; !exists && len(c.m) >= maxStaticCacheEntries {
		for old := range c.m { // arbitrary eviction; cap is a safety net
			delete(c.m, old)
			break
		}
	}
	f.sysProps = maps.Clone(f.sysProps)
	c.m[k] = f
}

// Prune drops entries that do not match a live (pid, start time) pair.
func (c *StaticCache) Prune(live map[int]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if start, ok := live[k.pid]; !ok || start != k.start {
			delete(c.m, k)
		}
	}
}

// Len reports the number of cached processes.
func (c *StaticCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// processStartTimes returns pid -> start time (`ps -o lstart=`, the same on
// macOS and procps Linux) for the given pids in one subprocess. Pids ps does
// not know are omitted. Cost is ~10 ms versus ~250-450 ms per jcmd probe.
func processStartTimes(run CmdRunner, pids []int) map[int]string {
	if len(pids) == 0 {
		return nil
	}
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	out, err := run("ps", "-o", "pid=,lstart=", "-p", strings.Join(ids, ","))
	if err != nil && len(out) == 0 {
		// ps exits 1 when any listed pid is gone, and proc.Output drops
		// stdout on a non-zero exit, so ask for each pid on its own.
		if len(pids) == 1 {
			return nil
		}
		m := make(map[int]string, len(pids))
		for _, p := range pids {
			for pid, start := range processStartTimes(run, []int{p}) {
				m[pid] = start
			}
		}
		return m
	}
	return parseStartTimes(out, len(pids))
}

func parseStartTimes(out []byte, n int) map[int]string {
	m := make(map[int]string, n)
	for _, line := range strings.Split(string(out), "\n") {
		pidStr, start, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		pid, perr := strconv.Atoi(pidStr)
		start = strings.Join(strings.Fields(start), " ")
		if perr != nil || start == "" {
			continue
		}
		m[pid] = start
	}
	return m
}

func (o CollectOptions) staticCache() *StaticCache {
	if o.StaticCache != nil {
		return o.StaticCache
	}
	return DefaultStaticCache
}

// collectStatic returns the static facts for pid, from cache when start is
// known and cached, otherwise by running jcmd (and caching when start is known).
func collectStatic(pid int, start string, run CmdRunner, cache *StaticCache) staticFacts {
	k := staticKey{pid, start}
	if start != "" {
		if f, ok := cache.get(k); ok {
			return f
		}
	}
	var f staticFacts
	f.sysProps = collectSysProps(pid, run)
	f.vmFlags, f.vmArgs, f.haveArgs = collectCommandLineOK(pid, run)
	f.haveProp = f.sysProps != nil
	// Only cache complete results; a failed probe must be retried.
	if start != "" && f.haveProp && f.haveArgs {
		cache.put(k, f)
	}
	return f
}
