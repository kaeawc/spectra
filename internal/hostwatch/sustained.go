package hostwatch

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	sustainedKeyPrefix = "cpu.sustained:"
	// startTolerance absorbs the one-second resolution of ps etime when a
	// process start time is derived as now-etime on successive samples.
	startTolerance = 3 * time.Second
	// sustainedMinGap is the floor for the suspend-gap threshold (see maxGap):
	// a longer gap between observations means the daemon was suspended or the
	// host slept, and is never counted as continuous load.
	sustainedMinGap = 5 * time.Minute
	// sustainedMinClearPoints is how many samples must be in the window before
	// a still-active finding can be disproved (capped by how many the window can
	// hold, see minClearPoints).
	sustainedMinClearPoints = 4
	// sustainedClearFraction is the hysteresis floor: an active finding keeps
	// firing until fewer than this share of window samples are above threshold.
	sustainedClearFraction = 0.5
)

// ProcObs is one process observation fed to the SustainedTracker.
type ProcObs struct {
	PID     int
	PPID    int
	Name    string
	CPUPct  float64
	Started time.Time
	// Skip marks a process that is present but has no usable CPU reading this
	// tick (for example a transient /proc read failure). The tracker keeps its
	// state instead of treating it as exited.
	Skip bool
}

// SustainedFinding describes a process that has stayed above the CPU
// threshold for the configured window.
type SustainedFinding struct {
	PID      int
	Name     string
	Started  time.Time
	Since    time.Time
	Duration time.Duration
	AvgCPU   float64
	Fraction float64
}

type cpuPoint struct {
	at    time.Time
	above bool
	cpu   float64
}

type sustainedEntry struct {
	name    string
	started time.Time
	points  []cpuPoint
	since   time.Time // zero until the criteria are first met
	// lastGood is when the entry last received a real CPU reading; Skip'd
	// ticks do not advance it, so a permanently unreadable process ages out.
	lastGood time.Time
	// adopt marks an entry seeded from a restored alert: its start time is
	// unknown until the process is next seen.
	adopt bool
}

// SustainedTracker is a pure, clock-free state machine: the caller supplies
// the observation time. Processes are keyed by PID plus start time so a
// reused PID starts a fresh history.
type SustainedTracker struct {
	cfg     SustainedConfig
	entries map[int]*sustainedEntry
	last    time.Time
}

func NewSustainedTracker(cfg SustainedConfig) *SustainedTracker {
	return &SustainedTracker{cfg: cfg, entries: map[int]*sustainedEntry{}}
}

// Observe records a full process snapshot taken at the given time and returns
// the processes currently considered sustained, ordered by PID.
func (t *SustainedTracker) Observe(at time.Time, procs []ProcObs) []SustainedFinding {
	if !t.last.IsZero() && (at.Sub(t.last) > t.maxGap() || at.Before(t.last)) {
		t.resetAfterGap()
	}
	t.last = at
	seen := make(map[int]bool, len(procs))
	for _, p := range procs {
		if p.PID <= 0 || t.cfg.Allowed(p.Name) {
			continue
		}
		seen[p.PID] = true
		t.dropIfReused(p)
		if p.Skip {
			continue
		}
		t.record(at, p)
	}
	for pid := range t.entries {
		if !seen[pid] {
			delete(t.entries, pid)
		}
	}
	return t.evaluate(at)
}

// dropIfReused forgets an entry whose PID now belongs to a process with a
// different start time, whether or not this tick has a CPU reading for it.
func (t *SustainedTracker) dropIfReused(p ProcObs) {
	e := t.entries[p.PID]
	switch {
	case e == nil:
	case e.adopt && path.Base(p.Name) == path.Base(e.name):
		e.started, e.adopt = p.Started, false
	case e.adopt || !sameStart(e.started, p.Started):
		delete(t.entries, p.PID)
	}
}

// Seed registers a finding restored after a daemon restart as a carried entry:
// it keeps being reported (so its alert is not resolved) until fresh samples
// disprove it, exactly like an alert carried across a suspend gap.
func (t *SustainedTracker) Seed(at time.Time, pid int, name string, since time.Time) {
	t.entries[pid] = &sustainedEntry{name: name, since: since, lastGood: at, adopt: true}
}

// maxGap is the observation gap treated as a suspend. The service sets MaxGap
// from the worst-case backed-off tick; without it, the floor applies.
func (t *SustainedTracker) maxGap() time.Duration {
	if t.cfg.MaxGap > sustainedMinGap {
		return t.cfg.MaxGap
	}
	return sustainedMinGap
}

// resetAfterGap discards history that spans a suspend, so the gap is never
// counted as continuous load. Entries that were already firing keep their
// since-time and stay reported until fresh samples disprove them, so the
// alert does not resolve and refire under a new ID across sleep.
func (t *SustainedTracker) resetAfterGap() {
	for pid, e := range t.entries {
		if e.since.IsZero() {
			delete(t.entries, pid)
			continue
		}
		e.points = nil
	}
}

// capacity is how many samples the window holds at the worst-case effective
// interval (4x the base, self-guard backoff); zero when the interval is unknown.
func (t *SustainedTracker) capacity() int {
	if t.cfg.Interval <= 0 {
		return 0
	}
	return int(t.cfg.Window / (4 * t.cfg.Interval))
}

// minClearPoints is the number of in-window samples required before a firing
// finding may be disproved: sustainedMinClearPoints, capped by what the window
// holds, but never below two so one low sample cannot clear an alert.
func (t *SustainedTracker) minClearPoints() int {
	n := sustainedMinClearPoints
	if c := t.capacity(); c > 0 && c < n {
		n = c
	}
	if n < 2 {
		n = 2
	}
	return n
}

// minAbovePoints is how many above-threshold samples firing needs: at least
// three, or half the window's capacity at the effective interval.
func (t *SustainedTracker) minAbovePoints() int {
	n := 3
	if c := t.capacity() / 2; c > n {
		n = c
	}
	return n
}

// trimPoints drops samples older than the window but keeps the newest of them
// as an anchor. Coverage is then measured from the anchor, so tick jitter
// (the period is interval plus work) can never make a full window unreachable.
func trimPoints(points []cpuPoint, cutoff time.Time) []cpuPoint {
	k := 0
	for k+1 < len(points) && points[k+1].at.Before(cutoff) {
		k++
	}
	return points[k:]
}

// inWindow returns the samples inside [cutoff, now], excluding the anchor.
func inWindow(points []cpuPoint, cutoff time.Time) []cpuPoint {
	if len(points) > 0 && points[0].at.Before(cutoff) {
		return points[1:]
	}
	return points
}

// staleHistory reports whether the entry's newest sample is too old to be
// continuous with a reading taken at the given time: a run of Skip'd ticks (for
// example an unreadable /proc) is not evidence of load, so the history before it
// must not stand in for window coverage. The limit is twice the worst-case
// backed-off tick; with no known interval nothing is considered stale here.
func (t *SustainedTracker) staleHistory(e *sustainedEntry, at time.Time) bool {
	if t.cfg.Interval <= 0 || len(e.points) == 0 {
		return false
	}
	return at.Sub(e.points[len(e.points)-1].at) > 8*t.cfg.Interval
}

func (t *SustainedTracker) record(at time.Time, p ProcObs) {
	e := t.entries[p.PID]
	above := p.CPUPct >= t.cfg.CPUPct
	if e == nil {
		if !above {
			return
		}
		e = &sustainedEntry{name: p.Name, started: p.Started}
		t.entries[p.PID] = e
	}
	if t.staleHistory(e, at) {
		e.points = nil
	}
	e.lastGood = at
	cutoff := at.Add(-t.cfg.Window)
	e.points = trimPoints(append(e.points, cpuPoint{at: at, above: above, cpu: p.CPUPct}), cutoff)
	in := inWindow(e.points, cutoff)
	carried := !e.since.IsZero() && len(in) < t.minClearPoints()
	if !anyAbove(in) && !carried {
		delete(t.entries, p.PID)
	}
}

func (t *SustainedTracker) evaluate(at time.Time) []SustainedFinding {
	var out []SustainedFinding
	cutoff := at.Add(-t.cfg.Window)
	for pid, e := range t.entries {
		if at.Sub(e.lastGood) >= t.cfg.Window {
			delete(t.entries, pid) // unreadable for a whole window: age out
			continue
		}
		e.points = trimPoints(e.points, cutoff)
		in := inWindow(e.points, cutoff)
		if len(in) == 0 {
			continue // carried across a gap with no fresh sample yet
		}
		frac, avg := summarize(in)
		covered := at.Sub(e.points[0].at) >= t.cfg.Window
		switch {
		case e.since.IsZero() && covered && frac >= t.cfg.MinFraction && countAbove(in) >= t.minAbovePoints():
			e.since = in[0].at
		case !e.since.IsZero() && frac < sustainedClearFraction && len(in) >= t.minClearPoints() && lastBelow(in, 2):
			e.since = time.Time{}
		}
		if e.since.IsZero() {
			continue
		}
		out = append(out, SustainedFinding{PID: pid, Name: e.name, Started: e.started, Since: e.since, Duration: at.Sub(e.since), AvgCPU: avg, Fraction: frac})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func summarize(points []cpuPoint) (fraction, avg float64) {
	var above int
	var sum float64
	for _, p := range points {
		sum += p.cpu
		if p.above {
			above++
		}
	}
	n := float64(len(points))
	return float64(above) / n, sum / n
}

func countAbove(points []cpuPoint) int {
	n := 0
	for _, p := range points {
		if p.above {
			n++
		}
	}
	return n
}

// lastBelow reports whether the n most recent samples are all below threshold.
func lastBelow(points []cpuPoint, n int) bool {
	if len(points) < n {
		return false
	}
	for _, p := range points[len(points)-n:] {
		if p.above {
			return false
		}
	}
	return true
}

func anyAbove(points []cpuPoint) bool {
	for _, p := range points {
		if p.above {
			return true
		}
	}
	return false
}

func sameStart(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= startTolerance
}

// Allowed reports whether name matches an allowlist entry. Entries are
// case-insensitive path.Match patterns against the process base name.
func (c SustainedConfig) Allowed(name string) bool {
	base := strings.ToLower(path.Base(name))
	for _, pattern := range c.Allowlist {
		if ok, err := path.Match(strings.ToLower(pattern), base); err == nil && ok {
			return true
		}
	}
	return false
}

// ChainLink is one ancestor in a process's owner chain.
type ChainLink struct {
	PID  int
	Name string
}

// ParentChain walks PPIDs from pid up to maxDepth ancestors using a process
// table, returned outermost-first and ending with the process itself.
func ParentChain(table map[int]ProcObs, pid, maxDepth int) []ChainLink {
	var rev []ChainLink
	seen := map[int]bool{}
	for cur := pid; len(rev) <= maxDepth && !seen[cur]; {
		p, ok := table[cur]
		if !ok {
			break
		}
		seen[cur] = true
		rev = append(rev, ChainLink{PID: p.PID, Name: path.Base(p.Name)})
		if p.PPID <= 0 {
			break
		}
		cur = p.PPID
	}
	out := make([]ChainLink, len(rev))
	for i, l := range rev {
		out[len(rev)-1-i] = l
	}
	return out
}

func formatChain(chain []ChainLink) string {
	parts := make([]string, len(chain))
	for i, l := range chain {
		parts[i] = fmt.Sprintf("%s(%d)", l.Name, l.PID)
	}
	return strings.Join(parts, " > ")
}

const unifiedLogHint = "sustained load usually means a client is polling or streaming the unified log (`log stream`, `log show`, OSLogStore); look for those clients"

var offenderHints = map[string]string{
	"diagnosticd":  unifiedLogHint,
	"oslogservice": unifiedLogHint,
	"mds_stores":   "Spotlight is re-indexing or a client keeps triggering indexing; check `mdutil -s /` and recently changed large directories",
}

// OffenderHint returns known-offender guidance for a process name.
func OffenderHint(name string) string {
	return offenderHints[strings.ToLower(path.Base(name))]
}

// SuggestNextCommand returns the shell command most likely to explain the load.
func SuggestNextCommand(goos string, pid int) string {
	if goos == "darwin" {
		return fmt.Sprintf("sample %d 10", pid)
	}
	return fmt.Sprintf("top -H -p %d", pid)
}

// SustainedDetail renders the single-line alert detail.
func SustainedDetail(f SustainedFinding, at time.Time, chain []ChainLink, goos, sampleSummary string) string {
	parts := []string{
		fmt.Sprintf("%s (pid %d) averaged %.0f%% CPU for %s since %s", f.Name, f.PID, f.AvgCPU, f.Duration.Round(time.Minute), f.Since.UTC().Format(time.RFC3339)),
		"up " + at.Sub(f.Started).Round(time.Minute).String(),
	}
	if len(chain) > 0 {
		parts = append(parts, "owner chain: "+formatChain(chain))
	}
	if hint := OffenderHint(f.Name); hint != "" {
		parts = append(parts, "hint: "+hint)
	}
	if sampleSummary != "" {
		parts = append(parts, sampleSummary)
	}
	parts = append(parts, "next: "+SuggestNextCommand(goos, f.PID))
	return strings.Join(parts, "; ")
}
