package hostwatch

import (
	"context"
	"fmt"
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
	sustainedSampleTimeout  = 8 * time.Second
	sustainedPersistRefresh = 5 * time.Minute
)

// sustainedState is the Service-side state of the sustained-CPU probe.
type sustainedState struct {
	tracker   *SustainedTracker
	runner    proc.Runner
	os        hostos.Kind
	summaries map[string]string
	persisted map[string]time.Time
}

func newSustainedState(cfg SustainedConfig, runner proc.Runner, kind hostos.Kind) *sustainedState {
	return &sustainedState{tracker: NewSustainedTracker(cfg), runner: runner, os: hostos.Resolve(kind), summaries: map[string]string{}, persisted: map[string]time.Time{}}
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
// spending CPU: `sample` on macOS, per-thread CPU from ps on Linux.
func collectSampleSummary(ctx context.Context, runner proc.Runner, kind hostos.Kind, pid int) string {
	ctx, cancel := context.WithTimeout(ctx, sustainedSampleTimeout)
	defer cancel()
	if kind == hostos.Darwin {
		out, err := proc.Output(ctx, runner, "sample", strconv.Itoa(pid), "1")
		if err != nil {
			return ""
		}
		return summarizeDarwinSample(string(out))
	}
	out, err := proc.Output(ctx, runner, "ps", "-L", "-o", "tid=,pcpu=,comm=", "-p", strconv.Itoa(pid))
	if err != nil {
		return ""
	}
	return summarizeLinuxThreads(string(out))
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
				return truncateRunes("top of stack: "+strings.Join(picked, " | "), 240)
			}
		default:
			picked = append(picked, strings.Join(strings.Fields(l), " "))
			if len(picked) == 3 {
				return truncateRunes("top of stack: "+strings.Join(picked, " | "), 240)
			}
		}
	}
	if len(picked) > 0 {
		return truncateRunes("top of stack: "+strings.Join(picked, " | "), 240)
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

// sustainedConditions observes one process snapshot and renders conditions.
// The one-shot sample summary is collected the first time a finding appears
// and reused for the life of the alert.
func (s *Service) sustainedConditions(ctx context.Context, at time.Time) []Condition {
	st := s.sustained
	procs, err := listProcObs(ctx, st.runner, st.os, at)
	if err != nil {
		s.opts.Logger.Debug("sustained cpu probe unavailable", "error", err)
		return nil
	}
	table := make(map[int]ProcObs, len(procs))
	for _, p := range procs {
		table[p.PID] = p
	}
	findings := st.tracker.Observe(at, procs)
	live := make(map[string]bool, len(findings))
	conds := make([]Condition, 0, len(findings))
	for _, f := range findings {
		key := sustainedKey(f)
		live[key] = true
		summary, ok := st.summaries[key]
		if !ok {
			summary = collectSampleSummary(ctx, st.runner, st.os, f.PID)
			st.summaries[key] = summary
		}
		detail := SustainedDetail(f, at, ParentChain(table, f.PID, sustainedChainDepth), st.os.String(), summary)
		conds = append(conds, Condition{Key: key, Severity: "warn", Title: path.Base(f.Name) + " sustained high CPU", Detail: detail})
	}
	for key := range st.summaries {
		if !live[key] {
			delete(st.summaries, key)
		}
	}
	return conds
}

// TickSustained runs one sustained-CPU probe cycle at the given time.
func (s *Service) TickSustained(ctx context.Context, at time.Time) {
	if s.sustained == nil {
		return
	}
	s.sustMu.Lock()
	conds := s.sustainedConditions(ctx, at)
	s.sustMu.Unlock()
	s.mu.Lock()
	events := s.engine.EvaluateSustained(at, conds)
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

func (s *Service) persistSustained(ctx context.Context, at time.Time, events []Event, active []Alert) {
	st := s.sustained
	write := func(a Alert) {
		if err := s.opts.Store.UpsertAlert(ctx, toRow(a)); err != nil {
			s.opts.Logger.Error("sustained alert persistence failed", "error", err)
		}
	}
	for _, event := range events {
		write(event.Alert)
		if event.Type == "resolved" {
			delete(st.persisted, event.Alert.Key)
		} else {
			st.persisted[event.Alert.Key] = at
		}
	}
	// Detail carries the running duration, so refresh the stored row
	// periodically rather than on every tick.
	for _, a := range active {
		if last, ok := st.persisted[a.Key]; ok && at.Sub(last) < sustainedPersistRefresh {
			continue
		}
		write(a)
		st.persisted[a.Key] = at
	}
}
