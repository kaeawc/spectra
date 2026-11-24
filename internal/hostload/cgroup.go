package hostload

import (
	"bufio"
	"path"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/pressure"
)

// Cgroup is the slice of the host that this process's cgroup allows. It is
// reported only on Linux and only when a CPU quota or memory limit applies.
type Cgroup struct {
	Version int    `json:"version"` // 1 or 2
	Path    string `json:"path"`
	// CPUQuotaCPUs is quota/period of the tightest cpu.max (v2) or
	// cpu.cfs_quota_us (v1); absent when unlimited.
	CPUQuotaCPUs *float64 `json:"cpu_quota_cpus,omitempty"`
	// MemoryLimitBytes is the tightest memory.max or memory.high on the
	// cgroup or an ancestor (v2), or memory.limit_in_bytes (v1); absent when
	// unlimited. Used and available are measured where that limit is set.
	MemoryLimitBytes *uint64 `json:"memory_limit_bytes,omitempty"`
	// MemoryUsedBytes excludes inactive page cache, which the kernel reclaims
	// before enforcing the limit (the same working set docker stats shows).
	MemoryUsedBytes      *uint64 `json:"memory_used_bytes,omitempty"`
	MemoryAvailableBytes *uint64 `json:"memory_available_bytes,omitempty"`
	// CPUPressure and MemoryPressure are the cgroup's PSI "some" avg10
	// percentages (v2 only).
	CPUPressure    *float64 `json:"cpu_pressure,omitempty"`
	MemoryPressure *float64 `json:"memory_pressure,omitempty"`
}

const cgroupRoot = "/sys/fs/cgroup"

// v1 reports "no limit" as the largest page-aligned int64, not "max".
const v1Unlimited = uint64(1) << 62

// Cgroup returns the cgroup limits of this process (or of CgroupPID), or nil
// when the host is not Linux, the cgroup cannot be read, or no limit applies.
func (h Host) Cgroup() *Cgroup {
	if h.os() != hostos.Linux {
		return nil
	}
	self := "self"
	if h.CgroupPID > 0 {
		self = strconv.Itoa(h.CgroupPID)
	}
	data, err := h.readFile("/proc/" + self + "/cgroup")
	if err != nil {
		return nil
	}
	var cg *Cgroup
	if p, ok := pressure.V2Path(data); ok {
		cg = h.cgroupV2(p)
	}
	if cg == nil {
		cg = h.cgroupV1(string(data))
	}
	if cg == nil || (cg.CPUQuotaCPUs == nil && cg.MemoryLimitBytes == nil) {
		return nil
	}
	return cg
}

func (h Host) cgroupV2(p string) *Cgroup {
	rel, ok := h.v2Rel(p)
	if !ok {
		return nil
	}
	cg := &Cgroup{Version: 2, Path: p}
	var memAt string
	for q := rel; ; q = path.Dir(q) {
		dir := path.Join(cgroupRoot, q)
		if cpus, ok := h.v2Quota(dir); ok && (cg.CPUQuotaCPUs == nil || cpus < *cg.CPUQuotaCPUs) {
			cg.CPUQuotaCPUs = &cpus
		}
		for _, f := range []string{"memory.max", "memory.high"} {
			if v, ok := h.readUint(dir + "/" + f); ok && (cg.MemoryLimitBytes == nil || v < *cg.MemoryLimitBytes) {
				cg.MemoryLimitBytes, memAt = &v, dir
			}
		}
		if q == "/" {
			break
		}
	}
	if cg.MemoryLimitBytes != nil {
		h.fillMemoryUse(cg, memAt+"/memory.current", memAt+"/memory.stat", "inactive_file")
	}
	dir := path.Join(cgroupRoot, rel)
	cg.CPUPressure = h.psiAvg10(dir + "/cpu.pressure")
	cg.MemoryPressure = h.psiAvg10(dir + "/memory.pressure")
	return cg
}

// v2Rel is p under the mount, or "/" when the mount is a cgroup namespace
// root that does not show the host path p.
func (h Host) v2Rel(p string) (string, bool) {
	for _, rel := range []string{p, "/"} {
		if _, err := h.readFile(path.Join(cgroupRoot, rel, "cgroup.controllers")); err == nil {
			return path.Clean("/" + rel), true
		}
	}
	return "", false
}

// v2Quota parses cpu.max ("<quota> <period>" or "max <period>").
func (h Host) v2Quota(dir string) (float64, bool) {
	data, err := h.readFile(dir + "/cpu.max")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(data))
	if len(f) != 2 {
		return 0, false
	}
	return quotaCPUs(f[0], f[1])
}

func quotaCPUs(quota, period string) (float64, bool) {
	q, err1 := strconv.ParseFloat(quota, 64)
	per, err2 := strconv.ParseFloat(period, 64)
	if err1 != nil || err2 != nil || q <= 0 || per <= 0 {
		return 0, false
	}
	return q / per, true
}

// cgroupV1 reads the cpu and memory controllers of a v1 (or hybrid) host.
// Only the process's own cgroup is read; v1 ancestors are not walked.
func (h Host) cgroupV1(procCgroup string) *Cgroup {
	cpuDir, cpuPath, memDir, memPath := v1Dirs(procCgroup)
	if cpuDir == "" && memDir == "" {
		return nil
	}
	cg := &Cgroup{Version: 1, Path: memPath}
	if cg.Path == "" {
		cg.Path = cpuPath
	}
	if dir := h.existingDir(cpuDir, "cpu.cfs_period_us"); dir != "" {
		q, _ := h.readFile(dir + "/cpu.cfs_quota_us")
		p, _ := h.readFile(dir + "/cpu.cfs_period_us")
		if cpus, ok := quotaCPUs(strings.TrimSpace(string(q)), strings.TrimSpace(string(p))); ok {
			cg.CPUQuotaCPUs = &cpus
		}
	}
	if dir := h.existingDir(memDir, "memory.limit_in_bytes"); dir != "" {
		if v, ok := h.readUint(dir + "/memory.limit_in_bytes"); ok && v < v1Unlimited {
			cg.MemoryLimitBytes = &v
			h.fillMemoryUse(cg, dir+"/memory.usage_in_bytes", dir+"/memory.stat", "total_inactive_file")
		}
	}
	return cg
}

// v1Dirs maps the cpu and memory lines of /proc/self/cgroup
// ("<id>:<controllers>:<path>") to their directories under the mount.
func v1Dirs(procCgroup string) (cpuDir, cpuPath, memDir, memPath string) {
	for _, line := range strings.Split(procCgroup, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 || parts[1] == "" {
			continue
		}
		for _, c := range strings.Split(parts[1], ",") {
			switch c {
			case "cpu":
				cpuDir, cpuPath = path.Join(cgroupRoot, parts[1], parts[2]), parts[2]
			case "memory":
				memDir, memPath = path.Join(cgroupRoot, parts[1], parts[2]), parts[2]
			}
		}
	}
	return cpuDir, cpuPath, memDir, memPath
}

// existingDir returns dir, or its controller mount root when a container's
// namespaced mount hides the host path, whichever holds probe.
func (h Host) existingDir(dir, probe string) string {
	if dir == "" {
		return ""
	}
	if _, err := h.readFile(dir + "/" + probe); err == nil {
		return dir
	}
	// The mount root is the first element below cgroupRoot.
	rest := strings.TrimPrefix(dir, cgroupRoot+"/")
	mount := cgroupRoot + "/" + strings.SplitN(rest, "/", 2)[0]
	if mount != dir {
		if _, err := h.readFile(mount + "/" + probe); err == nil {
			return mount
		}
	}
	return ""
}

func (h Host) fillMemoryUse(cg *Cgroup, currentFile, statFile, inactiveKey string) {
	cur, ok := h.readUint(currentFile)
	if !ok {
		return
	}
	used := cur
	if inactive, ok := h.statValue(statFile, inactiveKey); ok && inactive <= cur {
		used = cur - inactive
	}
	avail := uint64(0)
	if *cg.MemoryLimitBytes > used {
		avail = *cg.MemoryLimitBytes - used
	}
	cg.MemoryUsedBytes, cg.MemoryAvailableBytes = &used, &avail
}

func (h Host) statValue(file, key string) (uint64, bool) {
	data, err := h.readFile(file)
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[0] == key {
			v, err := strconv.ParseUint(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// readUint reads a single unsigned number; "max" and unreadable files fail.
func (h Host) readUint(file string) (uint64, bool) {
	data, err := h.readFile(file)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return v, err == nil
}

func (h Host) psiAvg10(file string) *float64 {
	data, err := h.readFile(file)
	if err != nil {
		return nil
	}
	r, err := pressure.ParseResource(data)
	if err != nil {
		return nil
	}
	return &r.Some.Avg10
}

// effectiveSlice is min(host, cgroup) for CPUs and available memory, each
// nil when no cgroup limit applies to it or the host figure is unknown.
func effectiveSlice(cg *Cgroup, r Reading) (*float64, *uint64) {
	if cg == nil {
		return nil, nil
	}
	var cpus *float64
	if cg.CPUQuotaCPUs != nil {
		c := min(float64(r.CPUs), *cg.CPUQuotaCPUs)
		cpus = &c
	}
	var avail *uint64
	if cg.MemoryAvailableBytes != nil && r.MemKnown {
		a := min(r.AvailBytes, *cg.MemoryAvailableBytes)
		avail = &a
	}
	return cpus, avail
}
