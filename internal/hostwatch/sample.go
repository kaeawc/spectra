package hostwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/proc"
	"golang.org/x/sys/unix"
)

type LimitUsage struct {
	Current int     `json:"current"`
	Limit   int     `json:"limit"`
	Pct     float64 `json:"pct"`
}
type KindStat struct {
	Count  int     `json:"count"`
	CPUPct float64 `json:"cpu_pct"`
	RSSMB  float64 `json:"rss_mb"`
}
type ProcStat struct {
	PID    int     `json:"pid"`
	Name   string  `json:"name"`
	CPUPct float64 `json:"cpu_pct"`
	RSSMB  float64 `json:"rss_mb"`
}
type Sample struct {
	At               time.Time             `json:"at"`
	NCPU             int                   `json:"ncpu"`
	Load1            float64               `json:"load1"`
	Load5            float64               `json:"load5"`
	Load15           float64               `json:"load15"`
	MemoryPressure   string                `json:"memory_pressure"`
	SwapUsedMB       float64               `json:"swap_used_mb"`
	MemFreePct       float64               `json:"mem_free_pct"`
	Limits           map[string]LimitUsage `json:"limits"`
	DataFreeGB       float64               `json:"data_free_gb"`
	ThermalThrottled bool                  `json:"thermal_throttled"`
	Kinds            map[string]KindStat   `json:"kinds"`
	TopCPU           []ProcStat            `json:"top_cpu"`
	SelfCPUPct       float64               `json:"self_cpu_pct"`
	Spawn            *SpawnState           `json:"spawn,omitempty"`
}
type Collector struct {
	OS       hostos.Kind
	Runner   proc.Runner
	Clock    clock.Clock
	ReadFile func(string) ([]byte, error)
	Load     func() ([3]float64, error)
	Memory   func() (string, float64, float64, error)
	Limits   func(int) map[string]LimitUsage
	Disk     func() (float64, error)
	Thermal  func(context.Context) (bool, error)
	CPUTime  func() (time.Duration, error)
	NCPU     func() int
	Metrics  *metrics.Collector
	Logger   logger.Logger
	Spawn    func() *SpawnState
	mu       sync.Mutex
	lastCPU  time.Duration
	lastAt   time.Time
}

func (c *Collector) Collect(ctx context.Context) (Sample, error) {
	kind := hostos.Resolve(c.OS)
	clk := c.Clock
	if clk == nil {
		clk = clock.System{}
	}
	ncpu := c.NCPU
	if ncpu == nil {
		ncpu = runtime.NumCPU
	}
	s := Sample{At: clk.Now().UTC(), NCPU: ncpu(), MemoryPressure: "unknown", DataFreeGB: -1, Limits: map[string]LimitUsage{}, Kinds: map[string]KindStat{}}
	count, err := c.collectPS(ctx, kind, &s)
	if err != nil {
		if ctx.Err() != nil {
			return Sample{}, ctx.Err()
		}
		if c.Logger != nil {
			c.Logger.Warn("watch process sample unavailable", "error", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return Sample{}, err
	}
	c.collectLoadMemory(kind, &s)
	c.collectResources(ctx, kind, count, &s)
	c.collectSelf(&s)
	if err = ctx.Err(); err != nil {
		return Sample{}, err
	}
	return s, nil
}
func (c *Collector) runner() proc.Runner {
	if c.Runner != nil {
		return c.Runner
	}
	return proc.Default
}
func (c *Collector) readFile() func(string) ([]byte, error) {
	if c.ReadFile != nil {
		return c.ReadFile
	}
	return os.ReadFile
}
func (c *Collector) collectPS(ctx context.Context, kind hostos.Kind, s *Sample) (int, error) {
	args := []string{"-Ao", "pid=,ppid=,rss=,%cpu=,comm="}
	if kind == hostos.Linux {
		args[0] = "-eo"
	}
	out, err := proc.Output(ctx, c.runner(), "ps", args...)
	if err != nil {
		return 0, fmt.Errorf("hostwatch ps: %w", err)
	}
	javaArgs := c.javaArguments(ctx, out)
	return parseProcesses(out, javaArgs, s, c.Metrics), nil
}

func (c *Collector) javaArguments(ctx context.Context, out []byte) map[int]string {
	var pids []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || strings.ToLower(filepath.Base(strings.Join(f[4:], " "))) != "java" {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid > 0 {
			pids = append(pids, f[0])
		}
		if len(pids) == 64 {
			break
		}
	}
	if len(pids) == 0 {
		return nil
	}
	argv, err := proc.Output(ctx, c.runner(), "ps", "-o", "pid=,args=", "-p", strings.Join(pids, ","))
	if err != nil {
		if c.Logger != nil {
			c.Logger.Debug("watch java arguments unavailable", "error", fmt.Errorf("hostwatch java ps: %w", err))
		}
		return nil
	}
	result := make(map[int]string, len(pids))
	for _, line := range strings.Split(string(argv), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			result[pid] = strings.TrimSpace(line[len(f[0]):])
		}
	}
	return result
}
func (c *Collector) collectLoadMemory(kind hostos.Kind, s *Sample) {
	load := c.Load
	if load == nil {
		load = func() ([3]float64, error) { return platformLoad(kind, c.readFile()) }
	}
	if v, err := load(); err == nil {
		s.Load1, s.Load5, s.Load15 = v[0], v[1], v[2]
	}
	mem := c.Memory
	if mem == nil {
		mem = func() (string, float64, float64, error) { return platformMemory(kind, c.readFile()) }
	}
	if p, swap, free, err := mem(); err == nil {
		s.MemoryPressure, s.SwapUsedMB, s.MemFreePct = p, swap, free
	}
}
func (c *Collector) collectResources(ctx context.Context, kind hostos.Kind, count int, s *Sample) {
	uidCount := 0
	if c.Spawn != nil {
		if spawn := c.Spawn(); spawn != nil {
			uidCount = spawn.ProcsUID
			s.Spawn = spawn
		}
	}
	limits := c.Limits
	if limits == nil {
		limits = func(n int) map[string]LimitUsage { return platformLimits(kind, c.readFile(), n, uidCount) }
	}
	s.Limits = limits(count)
	disk := c.Disk
	if disk == nil {
		disk = func() (float64, error) {
			var st unix.Statfs_t
			path := "/"
			if kind == hostos.Darwin {
				path = "/System/Volumes/Data"
			}
			if err := unix.Statfs(path, &st); err != nil {
				return 0, err
			}
			return float64(st.Bavail) * float64(st.Bsize) / 1e9, nil
		}
	}
	if v, err := disk(); err == nil {
		s.DataFreeGB = v
	}
	thermal := c.Thermal
	if thermal == nil {
		thermal = func(ctx context.Context) (bool, error) { return platformThermal(ctx, kind, c.runner()) }
	}
	if v, err := thermal(ctx); err == nil {
		s.ThermalThrottled = v
	}
}
func (c *Collector) collectSelf(s *Sample) {
	cpu := c.CPUTime
	if cpu == nil {
		cpu = selfCPUTime
	}
	v, err := cpu()
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastAt.IsZero() && s.At.After(c.lastAt) && v >= c.lastCPU {
		s.SelfCPUPct = 100 * float64(v-c.lastCPU) / float64(s.At.Sub(c.lastAt))
	}
	c.lastAt, c.lastCPU = s.At, v
}
func parseProcesses(out []byte, javaArgs map[int]string, s *Sample, m *metrics.Collector) int {
	var top []ProcStat
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		rss, e2 := strconv.ParseInt(f[2], 10, 64)
		cpu, e3 := strconv.ParseFloat(strings.ReplaceAll(f[3], ",", "."), 64)
		if e1 != nil || e2 != nil || e3 != nil || pid <= 0 {
			continue
		}
		count++
		command := strings.Join(f[4:], " ")
		name := filepath.Base(command)
		kind := Classify(command, javaArgs[pid])
		k := s.Kinds[kind]
		k.Count++
		k.CPUPct += cpu
		k.RSSMB += float64(rss) / 1024
		s.Kinds[kind] = k
		top = append(top, ProcStat{pid, name, cpu, float64(rss) / 1024})
		if m != nil {
			m.Add(metrics.Sample{TakenAt: s.At, PID: pid, RSSKiB: rss, CPUPct: cpu})
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].CPUPct > top[j].CPUPct })
	if len(top) > 5 {
		top = top[:5]
	}
	s.TopCPU = top
	return count
}
func selfCPUTime() (time.Duration, error) {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		return 0, err
	}
	return time.Duration(r.Utime.Sec)*time.Second + time.Duration(r.Utime.Usec)*time.Microsecond + time.Duration(r.Stime.Sec)*time.Second + time.Duration(r.Stime.Usec)*time.Microsecond, nil
}
func usage(current, limit int) LimitUsage {
	u := LimitUsage{Current: current, Limit: limit}
	if limit > 0 {
		u.Pct = 100 * float64(current) / float64(limit)
	}
	return u
}
