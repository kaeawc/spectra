// Package pressure reads Linux pressure-stall information (PSI) and cgroup v2
// accounting. PSI answers "is the host (or this cgroup) stalled waiting on
// CPU, memory, or I/O right now"; the cgroup counters answer "did this
// workload hit its memory limit, get CPU-throttled, or get OOM-killed".
//
// Everything is plain file reads under injectable roots, so the parsers run
// against fixture trees on any host. No privilege is required for the host
// PSI files or for a cgroup the caller can read.
package pressure

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrUnavailable means the kernel does not expose the requested data: PSI
// disabled (CONFIG_PSI=n or psi=0) or the cgroup is not on a v2 hierarchy.
var ErrUnavailable = errors.New("pressure data unavailable")

// Line is one PSI line. Avg values are the percentage of wall time, over the
// trailing window, in which tasks were stalled.
type Line struct {
	Avg10     float64 `json:"avg10"`
	Avg60     float64 `json:"avg60"`
	Avg300    float64 `json:"avg300"`
	TotalUsec uint64  `json:"total_usec"`
}

// Resource is the PSI for one resource. "some" means at least one task was
// stalled; "full" means every non-idle task was stalled at once, which is
// when the resource is genuinely the bottleneck. CPU has no meaningful full
// line at host level.
type Resource struct {
	Some Line  `json:"some"`
	Full *Line `json:"full,omitempty"`
}

// PSI is the host-wide pressure for each resource.
type PSI struct {
	CPU    Resource `json:"cpu"`
	Memory Resource `json:"memory"`
	IO     Resource `json:"io"`
}

// ReadPSI reads <procRoot>/pressure/{cpu,memory,io}. procRoot is normally
// "/proc".
func ReadPSI(procRoot string) (PSI, error) {
	var p PSI
	for _, e := range []struct {
		name string
		dst  *Resource
	}{{"cpu", &p.CPU}, {"memory", &p.Memory}, {"io", &p.IO}} {
		data, err := os.ReadFile(filepath.Join(procRoot, "pressure", e.name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return PSI{}, fmt.Errorf("psi %s: %w", e.name, ErrUnavailable)
			}
			return PSI{}, fmt.Errorf("psi %s: %w", e.name, err)
		}
		r, err := parseResource(data)
		if err != nil {
			return PSI{}, fmt.Errorf("psi %s: %w", e.name, err)
		}
		*e.dst = r
	}
	return p, nil
}

func parseResource(data []byte) (Resource, error) {
	var r Resource
	seen := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		kind, rest, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		line, err := parseLine(rest)
		if err != nil {
			return Resource{}, err
		}
		switch kind {
		case "some":
			r.Some = line
			seen = true
		case "full":
			l := line
			r.Full = &l
		}
	}
	if !seen {
		return Resource{}, errors.New("no 'some' line")
	}
	return r, nil
}

func parseLine(s string) (Line, error) {
	var l Line
	for _, kv := range strings.Fields(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		var err error
		switch k {
		case "avg10":
			l.Avg10, err = strconv.ParseFloat(v, 64)
		case "avg60":
			l.Avg60, err = strconv.ParseFloat(v, 64)
		case "avg300":
			l.Avg300, err = strconv.ParseFloat(v, 64)
		case "total":
			l.TotalUsec, err = strconv.ParseUint(v, 10, 64)
		}
		if err != nil {
			return Line{}, fmt.Errorf("parse %s: %w", kv, err)
		}
	}
	return l, nil
}

// Unlimited marks a cgroup limit written as "max".
const Unlimited int64 = -1

// MemoryEvents are the memory.events counters. They are cumulative since the
// cgroup was created, so any non-zero OOMKill means a process in the cgroup
// (or a descendant) was killed by the kernel for exceeding a limit.
type MemoryEvents struct {
	Low     uint64 `json:"low"`
	High    uint64 `json:"high"`
	Max     uint64 `json:"max"`
	OOM     uint64 `json:"oom"`
	OOMKill uint64 `json:"oom_kill"`
}

// Memory is the cgroup v2 memory controller view.
type Memory struct {
	Current     uint64       `json:"current_bytes"`
	Max         int64        `json:"max_bytes"`
	High        int64        `json:"high_bytes"`
	SwapCurrent uint64       `json:"swap_current_bytes"`
	Anon        uint64       `json:"anon_bytes"`
	File        uint64       `json:"file_bytes"`
	Events      MemoryEvents `json:"events"`
	Pressure    *Resource    `json:"pressure,omitempty"`
	// EffectiveMax is the tightest memory.max on this cgroup or any
	// ancestor (a limit set on a parent service applies to its children);
	// LimitedBy names the cgroup it came from. Unlimited when none is set.
	EffectiveMax int64  `json:"effective_max_bytes"`
	LimitedBy    string `json:"limited_by,omitempty"`
}

// CPU is the cgroup v2 cpu controller view.
type CPU struct {
	UsageUsec     uint64    `json:"usage_usec"`
	UserUsec      uint64    `json:"user_usec"`
	SystemUsec    uint64    `json:"system_usec"`
	NrPeriods     uint64    `json:"nr_periods"`
	NrThrottled   uint64    `json:"nr_throttled"`
	ThrottledUsec uint64    `json:"throttled_usec"`
	QuotaUsec     int64     `json:"quota_usec"`
	PeriodUsec    int64     `json:"period_usec"`
	Pressure      *Resource `json:"pressure,omitempty"`
}

// Pids is the cgroup v2 pids controller view.
type Pids struct {
	Current int64 `json:"current"`
	Max     int64 `json:"max"`
}

// Cgroup is the accounting for one cgroup v2 directory. A nil section means
// that controller is not enabled for the cgroup.
type Cgroup struct {
	Path       string    `json:"path"`
	Memory     *Memory   `json:"memory,omitempty"`
	CPU        *CPU      `json:"cpu,omitempty"`
	Pids       *Pids     `json:"pids,omitempty"`
	IOPressure *Resource `json:"io_pressure,omitempty"`
}

// ReadCgroup finds pid's cgroup v2 path in <procRoot>/<pid>/cgroup and reads
// it from cgroupRoot (normally "/sys/fs/cgroup"). A host still on cgroup v1
// (or a hybrid layout whose unified directory has no controllers) returns
// ErrUnavailable.
func ReadCgroup(procRoot, cgroupRoot string, pid int) (Cgroup, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return Cgroup{}, fmt.Errorf("read cgroup of pid %d: %w", pid, err)
	}
	p, ok := v2Path(data)
	if !ok {
		return Cgroup{}, fmt.Errorf("pid %d has no cgroup v2 path: %w", pid, ErrUnavailable)
	}
	return ReadCgroupPath(cgroupRoot, p)
}

// v2Path returns the path from the unified-hierarchy "0::<path>" line.
func v2Path(data []byte) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			// The kernel appends " (deleted)" for a removed cgroup.
			p = strings.TrimSuffix(strings.TrimSpace(p), " (deleted)")
			return path.Clean("/" + p), true
		}
	}
	return "", false
}

// ReadCgroupPath reads the cgroup at cgroupRoot+cgPath.
func ReadCgroupPath(cgroupRoot, cgPath string) (Cgroup, error) {
	dir := filepath.Join(cgroupRoot, filepath.FromSlash(cgPath))
	if _, err := os.Stat(filepath.Join(dir, "cgroup.controllers")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Cgroup{}, fmt.Errorf("%s is not a cgroup v2 directory: %w", dir, ErrUnavailable)
		}
		return Cgroup{}, fmt.Errorf("stat cgroup %s: %w", dir, err)
	}
	cg := Cgroup{Path: cgPath}
	cg.Memory = readMemory(cgroupRoot, cgPath)
	cg.CPU = readCPU(dir)
	cg.Pids = readPids(dir)
	cg.IOPressure = readPressureFile(filepath.Join(dir, "io.pressure"))
	return cg, nil
}

func readMemory(cgroupRoot, cgPath string) *Memory {
	dir := filepath.Join(cgroupRoot, filepath.FromSlash(cgPath))
	cur, ok := readUint(filepath.Join(dir, "memory.current"))
	if !ok {
		return nil
	}
	m := &Memory{Current: cur, Max: Unlimited, High: Unlimited, EffectiveMax: Unlimited}
	m.Max = readLimit(filepath.Join(dir, "memory.max"))
	m.High = readLimit(filepath.Join(dir, "memory.high"))
	m.SwapCurrent, _ = readUint(filepath.Join(dir, "memory.swap.current"))
	if kv := readKV(filepath.Join(dir, "memory.stat")); kv != nil {
		m.Anon, m.File = kv["anon"], kv["file"]
	}
	if kv := readKV(filepath.Join(dir, "memory.events")); kv != nil {
		m.Events = MemoryEvents{Low: kv["low"], High: kv["high"], Max: kv["max"], OOM: kv["oom"], OOMKill: kv["oom_kill"]}
	}
	m.Pressure = readPressureFile(filepath.Join(dir, "memory.pressure"))
	m.EffectiveMax, m.LimitedBy = effectiveMemoryMax(cgroupRoot, cgPath)
	return m
}

// effectiveMemoryMax walks from cgPath up to the root and returns the
// smallest memory.max it finds.
func effectiveMemoryMax(cgroupRoot, cgPath string) (int64, string) {
	best, from := Unlimited, ""
	for p := cgPath; ; p = path.Dir(p) {
		if v := readLimit(filepath.Join(cgroupRoot, filepath.FromSlash(p), "memory.max")); v != Unlimited && (best == Unlimited || v < best) {
			best, from = v, p
		}
		if p == "/" || p == "." {
			return best, from
		}
	}
}

func readCPU(dir string) *CPU {
	kv := readKV(filepath.Join(dir, "cpu.stat"))
	if kv == nil {
		return nil
	}
	c := &CPU{
		UsageUsec: kv["usage_usec"], UserUsec: kv["user_usec"], SystemUsec: kv["system_usec"],
		NrPeriods: kv["nr_periods"], NrThrottled: kv["nr_throttled"], ThrottledUsec: kv["throttled_usec"],
		QuotaUsec: Unlimited,
	}
	if data, err := os.ReadFile(filepath.Join(dir, "cpu.max")); err == nil {
		f := strings.Fields(string(data))
		if len(f) == 2 {
			if f[0] != "max" {
				if q, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					c.QuotaUsec = q
				}
			}
			c.PeriodUsec, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	c.Pressure = readPressureFile(filepath.Join(dir, "cpu.pressure"))
	return c
}

func readPids(dir string) *Pids {
	cur, ok := readUint(filepath.Join(dir, "pids.current"))
	if !ok {
		return nil
	}
	return &Pids{Current: int64(cur), Max: readLimit(filepath.Join(dir, "pids.max"))} // #nosec G115 -- a pid count cannot approach MaxInt64
}

// readLimit parses a limit file whose value is a number or "max".
func readLimit(file string) int64 {
	data, err := os.ReadFile(file)
	if err != nil {
		return Unlimited
	}
	s := strings.TrimSpace(string(data))
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return Unlimited
	}
	return v
}

func readUint(file string) (uint64, bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return v, err == nil
}

// readKV parses a flat "key value" file. It returns nil when the file cannot
// be read.
func readKV(file string) map[string]uint64 {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	out := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if v, err := strconv.ParseUint(f[1], 10, 64); err == nil {
			out[f[0]] = v
		}
	}
	return out
}

func readPressureFile(file string) *Resource {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	r, err := parseResource(data)
	if err != nil {
		return nil
	}
	return &r
}
