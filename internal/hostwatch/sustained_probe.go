package hostwatch

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

const (
	sustainedChainDepth     = 8
	sustainedSampleTimeout  = 3 * time.Second
	sustainedPersistRefresh = 5 * time.Minute
	// sustainedSampleRetry is how long a failed sample is remembered before
	// another attempt, so a privilege failure is not retried every tick.
	sustainedSampleRetry = 30 * time.Minute
	// linuxClockTicks is USER_HZ, the unit of utime/stime in /proc/<pid>/stat.
	// 100 is assumed; it is the value on every mainstream Linux build.
	linuxClockTicks = 100
	// sustainedProbeFailLimit is how many consecutive failed ps probes make the
	// firing alerts carry a "probe unavailable" note.
	sustainedProbeFailLimit = 5
)

type sampleSummary struct {
	text   string
	failed bool
	at     time.Time
}

type cpuSnap struct {
	started time.Time
	cpu     time.Duration
	at      time.Time
}

// sustainedState is the Service-side state of the sustained-CPU probe.
type sustainedState struct {
	tracker   *SustainedTracker
	runner    proc.Runner
	read      func(string) ([]byte, error)
	os        hostos.Kind
	summaries map[string]sampleSummary
	persisted map[string]time.Time
	prev      map[int]cpuSnap
	// failures counts consecutive failed ps probes.
	failures int
}

func newSustainedState(cfg SustainedConfig, runner proc.Runner, read func(string) ([]byte, error), kind hostos.Kind) *sustainedState {
	if read == nil {
		read = os.ReadFile
	}
	return &sustainedState{tracker: NewSustainedTracker(cfg), runner: runner, read: read, os: hostos.Resolve(kind), summaries: map[string]sampleSummary{}, persisted: map[string]time.Time{}, prev: map[int]cpuSnap{}}
}

// readLinuxCPU returns cumulative user+system CPU time from /proc/<pid>/stat.
func readLinuxCPU(read func(string) ([]byte, error), pid int) (time.Duration, error) {
	b, err := read(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("sustained stat pid %d: %w", pid, err)
	}
	text := string(b)
	right := strings.LastIndexByte(text, ')')
	if right < 0 {
		return 0, fmt.Errorf("sustained stat pid %d: malformed", pid)
	}
	f := strings.Fields(text[right+1:])
	if len(f) < 13 {
		return 0, fmt.Errorf("sustained stat pid %d: short", pid)
	}
	utime, e1 := strconv.ParseInt(f[11], 10, 64)
	stime, e2 := strconv.ParseInt(f[12], 10, 64)
	if e1 != nil || e2 != nil {
		return 0, fmt.Errorf("sustained stat pid %d: bad cpu fields", pid)
	}
	// Scale by the exact per-tick duration (10ms): multiplying by time.Second
	// before dividing overflows int64 after ~1,067 CPU-days.
	return time.Duration(utime+stime) * (time.Second / linuxClockTicks), nil
}

// cpuObservations turns a process snapshot into tracker input. On Linux ps
// reports lifetime-average CPU, so usage is the delta of cumulative CPU time
// between successive samples of the same PID and start time; a first sighting
// yields no observation. On macOS ps %cpu is a decayed recent value and is
// used as is.
func (st *sustainedState) cpuObservations(procs []ProcObs, at time.Time) []ProcObs {
	if st.os != hostos.Linux {
		return procs
	}
	out := make([]ProcObs, 0, len(procs))
	next := make(map[int]cpuSnap, len(procs))
	for _, p := range procs {
		cpu, err := readLinuxCPU(st.read, p.PID)
		if err != nil {
			// ps still lists the process: keep its tracker state and its
			// previous snapshot so the next tick can still produce a delta.
			if prev, ok := st.prev[p.PID]; ok {
				next[p.PID] = prev
			}
			p.Skip = true
			out = append(out, p)
			continue
		}
		next[p.PID] = cpuSnap{started: p.Started, cpu: cpu, at: at}
		prev, ok := st.prev[p.PID]
		if !ok || !sameStart(prev.started, p.Started) || !at.After(prev.at) || cpu < prev.cpu {
			continue
		}
		p.CPUPct = 100 * float64(cpu-prev.cpu) / float64(at.Sub(prev.at))
		out = append(out, p)
	}
	st.prev = next
	return out
}

func (s *Service) initSustained() {
	if collector, ok := s.opts.Collector.(*Collector); ok && s.opts.Runner == nil {
		s.opts.Runner, s.opts.OS = collector.runner(), collector.OS
	}
	if s.opts.Runner == nil {
		return
	}
	if s.opts.Config.Sustained.Window <= 0 {
		s.opts.Config.Sustained = DefaultSustainedConfig()
	}
	if err := s.opts.Config.ValidateWatchInterval(s.opts.Interval); err != nil {
		s.opts.Logger.Warn("watch config", "error", err)
	}
	cfg := s.opts.Config.Sustained
	// Self-guard backoff can stretch the tick to 4x the base interval, so the
	// suspend gap is sized for that worst case; coverage uses the base interval.
	cfg.Interval = s.opts.Interval
	cfg.MaxGap = 12 * s.opts.Interval
	read := s.opts.ReadFile
	if c, ok := s.opts.Collector.(*Collector); ok && read == nil {
		read = c.readFile()
	}
	s.sustained = newSustainedState(cfg, s.opts.Runner, read, s.opts.OS)
}

// parseEtime converts ps etime ([[dd-]hh:]mm:ss) to a duration.
func parseEtime(v string) (time.Duration, error) {
	days := 0
	if d, rest, ok := strings.Cut(v, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, fmt.Errorf("etime days %q: %w", v, err)
		}
		days, v = n, rest
	}
	parts := strings.Split(v, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("etime %q: unexpected format", v)
	}
	total := days
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("etime %q: bad field", v)
		}
		secs = secs*60 + n
	}
	return time.Duration(total)*24*time.Hour + time.Duration(secs)*time.Second, nil
}

// parseProcObs parses `ps -o pid=,ppid=,%cpu=,etime=,comm=` output.
func parseProcObs(out []byte, at time.Time) []ProcObs {
	var procs []ProcObs
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		cpu, e3 := strconv.ParseFloat(strings.ReplaceAll(f[2], ",", "."), 64)
		age, e4 := parseEtime(f[3])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || pid <= 0 {
			continue
		}
		procs = append(procs, ProcObs{PID: pid, PPID: ppid, CPUPct: cpu, Name: strings.Join(f[4:], " "), Started: at.Add(-age).Truncate(time.Second)})
	}
	return procs
}

func listProcObs(ctx context.Context, runner proc.Runner, kind hostos.Kind, at time.Time) ([]ProcObs, error) {
	flag := "-Ao"
	if kind == hostos.Linux {
		flag = "-eo"
	}
	out, err := proc.Output(ctx, runner, "ps", flag, "pid=,ppid=,%cpu=,etime=,comm=")
	if err != nil {
		return nil, fmt.Errorf("sustained ps: %w", err)
	}
	return parseProcObs(out, at), nil
}

// collectSampleSummary takes a one-shot, bounded look at where a process is
// spending CPU: `sample` on macOS, per-thread CPU from ps on Linux. ok is
// false, with an explanatory text, when the look could not be taken (for
// example `sample` needs root for daemons owned by root).
func collectSampleSummary(ctx context.Context, runner proc.Runner, kind hostos.Kind, pid int) (text string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, sustainedSampleTimeout)
	defer cancel()
	if kind == hostos.Darwin {
		if out, err := proc.Output(ctx, runner, "sample", strconv.Itoa(pid), "1"); err == nil {
			if text = summarizeDarwinSample(string(out)); text != "" {
				return text, true
			}
		}
		return fmt.Sprintf("sample unavailable (needs root: run `sudo sample %d 10`)", pid), false
	}
	if out, err := proc.Output(ctx, runner, "ps", "-L", "-o", "tid=,pcpu=,comm=", "-p", strconv.Itoa(pid)); err == nil {
		if text = summarizeLinuxThreads(string(out)); text != "" {
			return text, true
		}
	}
	return "thread summary unavailable", false
}

func summarizeDarwinSample(out string) string {
	lines := strings.Split(out, "\n")
	var picked []string
	in := false
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "Sort by top of stack"):
			in = true
		case !in:
		case strings.TrimSpace(l) == "":
			if len(picked) > 0 {
				return truncateRunes("sample top of stack: "+strings.Join(picked, " | "), 240)
			}
		default:
			picked = append(picked, strings.Join(strings.Fields(l), " "))
			if len(picked) == 3 {
				return truncateRunes("sample top of stack: "+strings.Join(picked, " | "), 240)
			}
		}
	}
	if len(picked) > 0 {
		return truncateRunes("sample top of stack: "+strings.Join(picked, " | "), 240)
	}
	return ""
}

func summarizeLinuxThreads(out string) string {
	type thread struct {
		tid, name string
		cpu       float64
	}
	var threads []thread
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		if cpu, err := strconv.ParseFloat(f[1], 64); err == nil {
			threads = append(threads, thread{f[0], strings.Join(f[2:], " "), cpu})
		}
	}
	sort.Slice(threads, func(i, j int) bool { return threads[i].cpu > threads[j].cpu })
	var parts []string
	for i, t := range threads {
		if i == 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("tid %s %s %.0f%%", t.tid, t.name, t.cpu))
	}
	if len(parts) == 0 {
		return ""
	}
	return "busiest threads: " + strings.Join(parts, " | ")
}

func sustainedKey(f SustainedFinding) string {
	return fmt.Sprintf("%s%s:%d", sustainedKeyPrefix, path.Base(f.Name), f.PID)
}

// sampleFor returns the cached one-shot summary for a finding. A missing or
// stale-failed entry is refreshed only while budget remains (one attempt per
// tick, so slow `sample` calls never stack up on the watch tick).
func (st *sustainedState) sampleFor(ctx context.Context, key string, pid int, at time.Time, budget *int) string {
	cached, have := st.summaries[key]
	retry := have && cached.failed && at.Sub(cached.at) >= sustainedSampleRetry
	if (!have || retry) && *budget > 0 {
		*budget--
		text, ok := collectSampleSummary(ctx, st.runner, st.os, pid)
		cached = sampleSummary{text: text, failed: !ok, at: at}
		st.summaries[key] = cached
	}
	return cached.text
}

// sustainedConditions observes one process snapshot and renders conditions.
// The sample summary is collected once per alert (failures are retried after
// a long backoff) and reused for the life of the alert.
func (s *Service) sustainedConditions(ctx context.Context, at time.Time) (conds []Condition, ok bool) {
	st := s.sustained
	procs, err := listProcObs(ctx, st.runner, st.os, at)
	if err != nil {
		st.failures++
		switch {
		case st.failures == sustainedProbeFailLimit:
			s.opts.Logger.Warn("sustained cpu probe unavailable; firing alerts are flagged and no longer refreshed", "error", err, "consecutive_failures", st.failures)
		case st.failures > sustainedProbeFailLimit && st.failures%sustainedProbeFailLimit == 0:
			s.opts.Logger.Warn("sustained cpu probe still unavailable", "error", err, "consecutive_failures", st.failures)
		default:
			s.opts.Logger.Debug("sustained cpu probe unavailable", "error", err)
		}
		return nil, false
	}
	if st.failures >= sustainedProbeFailLimit {
		s.opts.Logger.Info("sustained cpu probe recovered", "consecutive_failures", st.failures)
	}
	st.failures = 0
	table := make(map[int]ProcObs, len(procs))
	for _, p := range procs {
		table[p.PID] = p
	}
	findings := st.tracker.Observe(at, st.cpuObservations(procs, at))
	live := make(map[string]bool, len(findings))
	conds = make([]Condition, 0, len(findings))
	budget := 1
	for _, f := range findings {
		key := sustainedKey(f)
		live[key] = true
		summary := st.sampleFor(ctx, key, f.PID, at, &budget)
		detail := SustainedDetail(f, at, ParentChain(table, f.PID, sustainedChainDepth), st.os.String(), summary)
		conds = append(conds, Condition{Key: key, Severity: "warn", Title: path.Base(f.Name) + " sustained high CPU", Detail: detail})
	}
	for key := range st.summaries {
		if !live[key] {
			delete(st.summaries, key)
		}
	}
	return conds, true
}

// seedSustained registers restored sustained alerts with the tracker.
func (s *Service) seedSustained(at time.Time, alerts []Alert) {
	if s.sustained == nil {
		return
	}
	s.sustMu.Lock()
	defer s.sustMu.Unlock()
	for _, a := range alerts {
		if a.State != "firing" || !strings.HasPrefix(a.Key, sustainedKeyPrefix) {
			continue
		}
		rest := strings.TrimPrefix(a.Key, sustainedKeyPrefix)
		i := strings.LastIndexByte(rest, ':')
		if i < 0 {
			continue
		}
		if pid, err := strconv.Atoi(rest[i+1:]); err == nil && pid > 0 {
			id := parseRestoredIdent(a.Detail)
			since := id.since
			if since.IsZero() || since.After(a.FiredAt) {
				since = a.FiredAt // legacy detail without a since-time
			}
			s.sustained.tracker.Seed(at, pid, rest[:i], since, id.started)
		}
	}
}

// TickSustained runs one sustained-CPU probe cycle at the given time.
func (s *Service) TickSustained(ctx context.Context, at time.Time) {
	if s.sustained == nil {
		return
	}
	s.sustMu.Lock()
	conds, ok := s.sustainedConditions(ctx, at)
	failures := s.sustained.failures
	s.sustMu.Unlock()
	if !ok {
		// No evidence this tick: leave alerts and tracker untouched, but once
		// the probe has been down for a while say so on the alerts themselves.
		if failures >= sustainedProbeFailLimit {
			s.flagProbeUnavailable(ctx, at)
		}
		return
	}
	s.mu.Lock()
	stale := staleKeys(s.engine.Active())
	events := s.engine.EvaluateSustained(at, conds)
	events = append(events, clearedNoteEvents(stale, s.engine.Active())...)
	var refresh []Alert
	for _, a := range s.engine.Active() {
		if strings.HasPrefix(a.Key, sustainedKeyPrefix) {
			refresh = append(refresh, a)
		}
	}
	for _, event := range events {
		s.fanoutLocked(notification{"alerts.event", event}, false)
	}
	s.mu.Unlock()
	s.persistSustained(ctx, at, events, refresh)
	for _, event := range events {
		if event.Type == "fired" {
			if err := s.opts.Notifier.Notify(ctx, event.Alert); err != nil {
				s.opts.Logger.Warn("sustained desktop notification failed", "error", err)
			}
		}
	}
}

// probeUnavailableNote marks firing alerts whose evidence has gone stale. It is
// constant so re-flagging is idempotent and the stored detail only changes once.
const probeUnavailableNote = " [probe unavailable: ps has failed for several consecutive ticks; this alert is not being re-evaluated and may be stale]"

func staleKeys(active []Alert) map[string]bool {
	out := map[string]bool{}
	for _, a := range active {
		if strings.HasPrefix(a.Key, sustainedKeyPrefix) && strings.HasSuffix(a.Detail, probeUnavailableNote) {
			out[a.Key] = true
		}
	}
	return out
}

// clearedNoteEvents reports an updated event for each alert that carried the
// stale note before the evaluation and no longer does. Detail-only changes emit
// no engine event of their own, and the cleared detail should reach subscribers
// and the store without waiting for the periodic refresh.
func clearedNoteEvents(stale map[string]bool, active []Alert) []Event {
	var out []Event
	for _, a := range active {
		if stale[a.Key] && !strings.HasSuffix(a.Detail, probeUnavailableNote) {
			out = append(out, Event{"updated", a})
		}
	}
	return out
}

// flagProbeUnavailable keeps every firing sustained alert (resolving it as
// healthy, or holding it silently forever, are both wrong without evidence) but
// appends probeUnavailableNote to its detail, tells subscribers, and persists
// the change. A write that fails is retried on later failed ticks.
func (s *Service) flagProbeUnavailable(ctx context.Context, at time.Time) {
	s.mu.Lock()
	var events []Event
	var noted []Alert
	fresh := map[string]bool{}
	for _, a := range s.engine.Active() {
		if !strings.HasPrefix(a.Key, sustainedKeyPrefix) {
			continue
		}
		if !strings.HasSuffix(a.Detail, probeUnavailableNote) {
			if upd, ok := s.engine.SetDetail(a.Key, a.Detail+probeUnavailableNote); ok {
				a = upd
				fresh[a.Key] = true
				events = append(events, Event{"updated", a})
				s.fanoutLocked(notification{"alerts.event", Event{"updated", a}}, false)
			}
		}
		noted = append(noted, a)
	}
	s.mu.Unlock()
	s.sustMu.Lock()
	defer s.sustMu.Unlock()
	for key := range fresh {
		delete(s.sustained.persisted, key) // force an immediate write
	}
	s.persistSustained(ctx, at, events, noted)
}

func (s *Service) persistSustained(ctx context.Context, at time.Time, events []Event, active []Alert) {
	st := s.sustained
	// write reports success; a failed write leaves the key out of persisted so
	// the next tick retries it.
	write := func(a Alert) bool {
		if err := s.opts.Store.UpsertAlert(ctx, toRow(a)); err != nil {
			s.opts.Logger.Error("sustained alert persistence failed", "error", err)
			return false
		}
		return true
	}
	for _, event := range events {
		if ok := write(event.Alert); ok && event.Type != "resolved" {
			st.persisted[event.Alert.Key] = at
		} else {
			delete(st.persisted, event.Alert.Key)
		}
	}
	// Detail carries the running duration, so refresh the stored row
	// periodically rather than on every tick.
	for _, a := range active {
		if last, ok := st.persisted[a.Key]; ok && at.Sub(last) < sustainedPersistRefresh {
			continue
		}
		if write(a) {
			st.persisted[a.Key] = at
		} else {
			delete(st.persisted, a.Key)
		}
	}
}
