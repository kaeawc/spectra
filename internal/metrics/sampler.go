package metrics

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CmdRunner abstracts subprocess calls for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

func defaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Sampler collects process metrics at a fixed interval and feeds them
// into a Collector. It uses ps -axwwo to gather RSS and CPU% for all
// processes, same format as the process collector.
type Sampler struct {
	collector *Collector
	interval  time.Duration
	run       CmdRunner
}

// NewSampler returns a Sampler that will push samples into c at the given
// interval. run may be nil (uses the real ps command).
func NewSampler(c *Collector, interval time.Duration, run CmdRunner) *Sampler {
	if run == nil {
		run = defaultRunner
	}
	return &Sampler{collector: c, interval: interval, run: run}
}

// Run starts the sampling loop. It blocks until ctx is cancelled.
func (s *Sampler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			s.sample(t)
		}
	}
}

// sample runs ps once and records one sample per process.
func (s *Sampler) sample(at time.Time) {
	// pid=, rss=, vsz=, pcpu=  — no comm= to avoid space issues
	out, err := s.run("ps", "-axwwo", "pid=,rss=,vsz=,pcpu=")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		sample, ok := parseSampleLine(line, at)
		if ok {
			s.collector.Add(sample)
		}
	}
}

// parseSampleLine parses one line of `ps -axwwo pid=,rss=,vsz=,pcpu=`.
//
// Example: "  412   12345  67890  1.2"
func parseSampleLine(line string, at time.Time) (Sample, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return Sample{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return Sample{}, false
	}
	rss, _ := strconv.ParseInt(fields[1], 10, 64)
	vsz, _ := strconv.ParseInt(fields[2], 10, 64)
	cpu, _ := strconv.ParseFloat(fields[3], 64)
	return Sample{
		TakenAt:  at,
		PID:      pid,
		RSSKiB:   rss,
		VSizeKiB: vsz,
		CPUPct:   cpu,
	}, true
}
