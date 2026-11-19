package thermal

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	thermalDir  = "sys/class/thermal"
	cpuDir      = "sys/devices/system/cpu"
	noTurboPath = "sys/devices/system/cpu/intel_pstate/no_turbo"
)

func readTrim(fsys fs.FS, p string) (string, bool) {
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

func readInt(fsys fs.FS, p string) (int64, bool) {
	s, ok := readTrim(fsys, p)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// dirsWithPrefix lists entries of dir whose name starts with prefix and is
// followed by digits only (thermal_zone0, cpu12; not cpufreq or cpuidle).
func dirsWithPrefix(fsys fs.FS, dir, prefix string) []string {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || n == "" {
			continue
		}
		if _, err := strconv.Atoi(n); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return numSuffix(names[i], prefix) < numSuffix(names[j], prefix) })
	return names
}

func numSuffix(name, prefix string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(name, prefix))
	return n
}

func (c *Collector) collectLinux() Report {
	fsys := c.fsys()
	r := Report{OS: "linux", Findings: []Finding{}}
	zoneHot := false
	if _, err := fs.Stat(fsys, thermalDir); err != nil {
		r.Notes = append(r.Notes, "/sys/class/thermal is not present (container or VM); no thermal zones to read")
	} else {
		readZones(fsys, &r)
		zoneHot = len(r.Findings) > 0
		readCooling(fsys, &r)
	}
	readFreqCaps(fsys, &r, zoneHot)
	r.Counters = readThrottleCounts(fsys)
	for p, n := range r.Counters {
		if n > 0 {
			r.Notes = append(r.Notes, fmt.Sprintf("%s is %d (cumulative since boot; use --watch to see whether it is rising)", p, n))
		}
	}
	sort.Strings(r.Notes)
	r.decide("no deciding thermal-throttling signal found (no thermal zone near a trip point, no throttle counter)")
	return r
}

func readZones(fsys fs.FS, r *Report) {
	for _, name := range dirsWithPrefix(fsys, thermalDir, "thermal_zone") {
		dir := path.Join(thermalDir, name)
		if mode, _ := readTrim(fsys, path.Join(dir, "mode")); mode == "disabled" {
			continue
		}
		milli, ok := readInt(fsys, path.Join(dir, "temp"))
		if !ok || milli <= 0 {
			continue
		}
		typ, _ := readTrim(fsys, path.Join(dir, "type"))
		z := Zone{Name: name, Type: typ, TempC: float64(milli) / 1000}
		for i := 0; ; i++ {
			tm, ok := readInt(fsys, path.Join(dir, fmt.Sprintf("trip_point_%d_temp", i)))
			if !ok {
				break
			}
			if tc := float64(tm) / 1000; tc <= 0 || tc > MaxPlausibleTripC {
				continue // firmware placeholder (0, -274000)
			}
			tt, _ := readTrim(fsys, path.Join(dir, fmt.Sprintf("trip_point_%d_type", i)))
			z.Trips = append(z.Trips, Trip{Type: tt, TempC: float64(tm) / 1000})
		}
		r.Zones = append(r.Zones, z)
		r.Findings = append(r.Findings, judgeZone(z)...)
	}
}

func zoneLabel(z Zone) string {
	if z.Type != "" {
		return z.Type
	}
	return z.Name
}

func judgeZone(z Zone) []Finding {
	var out []Finding
	for _, t := range z.Trips {
		switch t.Type {
		case "passive", "hot", "critical":
			if z.TempC >= t.TempC {
				out = append(out, Finding{Throttled, fmt.Sprintf("zone %s %.0f°C ≥ %s trip %.0f°C", zoneLabel(z), z.TempC, t.Type, t.TempC)})
			} else if z.TempC >= t.TempC-WarmMarginC {
				out = append(out, Finding{Warm, fmt.Sprintf("zone %s %.0f°C within %.0f°C of %s trip %.0f°C", zoneLabel(z), z.TempC, WarmMarginC, t.Type, t.TempC)})
			}
		case "active":
			if z.TempC >= t.TempC {
				out = append(out, Finding{Warm, fmt.Sprintf("zone %s %.0f°C ≥ active trip %.0f°C", zoneLabel(z), z.TempC, t.TempC)})
			}
		}
	}
	return out
}

func readCooling(fsys fs.FS, r *Report) {
	var notes []string
	for _, name := range dirsWithPrefix(fsys, thermalDir, "cooling_device") {
		dir := path.Join(thermalDir, name)
		typ, _ := readTrim(fsys, path.Join(dir, "type"))
		cur, ok := readInt(fsys, path.Join(dir, "cur_state"))
		if !ok {
			continue
		}
		maxS, _ := readInt(fsys, path.Join(dir, "max_state"))
		r.Cooling = append(r.Cooling, Cooling{Name: name, Type: typ, CurState: cur, MaxState: maxS})
		if cur <= 0 {
			continue
		}
		desc := fmt.Sprintf("cooling device %s (%s) cur_state %d/%d", name, typ, cur, maxS)
		// Processor and powerclamp devices cut CPU speed. Fans idle at low states
		// and firmware devices like intel_tcc_cooling hold a fixed offset, so
		// neither decides the verdict unless a fan is flat out.
		switch {
		case typ == "Processor" || typ == "intel_powerclamp":
			r.Findings = append(r.Findings, Finding{Throttled, desc})
		case strings.Contains(strings.ToLower(typ), "fan") && maxS > 0 && cur == maxS:
			r.Findings = append(r.Findings, Finding{Warm, desc + " (fan at maximum)"})
		default:
			notes = append(notes, desc+" (active, not a throttling signal)")
		}
	}
	r.Notes = append(r.Notes, notes...)
}

// readFreqCaps records CPUs capped below FreqCapRatio. A cap alone is only a
// note (no_turbo or a power profile causes it on a cold machine); it becomes
// throttled evidence when a thermal zone is already warm or worse.
func readFreqCaps(fsys fs.FS, r *Report, zoneHot bool) {
	for _, name := range dirsWithPrefix(fsys, cpuDir, "cpu") {
		dir := path.Join(cpuDir, name, "cpufreq")
		scaling, ok1 := readInt(fsys, path.Join(dir, "scaling_max_freq"))
		hw, ok2 := readInt(fsys, path.Join(dir, "cpuinfo_max_freq"))
		if !ok1 || !ok2 || hw <= 0 || float64(scaling) >= FreqCapRatio*float64(hw) {
			continue
		}
		r.FreqCaps = append(r.FreqCaps, FreqCap{CPU: name, ScalingKHz: scaling, MaxKHz: hw})
		if zoneHot {
			r.Findings = append(r.Findings, Finding{Throttled, fmt.Sprintf("%s scaling_max_freq %d kHz < %.0f%% of cpuinfo_max_freq %d kHz while a thermal zone is hot", name, scaling, FreqCapRatio*100, hw)})
		}
	}
	if len(r.FreqCaps) == 0 || zoneHot {
		return
	}
	c := r.FreqCaps[0]
	note := fmt.Sprintf("frequency capped at %d%% (%s scaling_max_freq %d kHz of %d kHz) — may be a power profile", c.ScalingKHz*100/c.MaxKHz, c.CPU, c.ScalingKHz, c.MaxKHz)
	if v, ok := readInt(fsys, noTurboPath); ok && v == 1 {
		note += "; intel_pstate no_turbo is 1"
	}
	r.Notes = append(r.Notes, note)
}

func readThrottleCounts(fsys fs.FS) map[string]int64 {
	out := map[string]int64{}
	for _, name := range dirsWithPrefix(fsys, cpuDir, "cpu") {
		for _, f := range []string{"core_throttle_count", "package_throttle_count"} {
			if n, ok := readInt(fsys, path.Join(cpuDir, name, "thermal_throttle", f)); ok {
				out[name+"/thermal_throttle/"+f] = n
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// counterTracker accumulates throttle-counter increases between consecutive
// readable observations, so a counter that later becomes unreadable keeps the
// increase already seen.
type counterTracker struct {
	prev map[string]int64
	inc  map[string]*CounterIncrease
}

func newCounterTracker(first map[string]int64) *counterTracker {
	t := &counterTracker{prev: map[string]int64{}, inc: map[string]*CounterIncrease{}}
	t.observe(first)
	return t
}

func (t *counterTracker) observe(cur map[string]int64) {
	for p, n := range cur {
		if from, ok := t.prev[p]; ok && n > from {
			if t.inc[p] == nil {
				t.inc[p] = &CounterIncrease{Path: p, From: from}
			}
			t.inc[p].To = n
		}
		t.prev[p] = n
	}
}

func (t *counterTracker) increases() []CounterIncrease {
	var out []CounterIncrease
	for _, v := range t.inc {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
