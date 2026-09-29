package hostwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type SpawnProcess struct {
	PID  int
	PPID int
	UID  int
	Comm string
}

type SpawnBackend interface {
	List() ([]SpawnProcess, error)
	Argv(int) (string, error)
	Limits() (uid, total int)
}

type SpawnParent struct {
	PID        int    `json:"pid"`
	Comm       string `json:"comm"`
	ChildCount int    `json:"child_count"`
	Argv       string `json:"argv"`
}

type SpawnCommand struct {
	Comm  string `json:"comm"`
	Count int    `json:"count"`
}

type SpawnState struct {
	At          time.Time      `json:"at"`
	ProcsTotal  int            `json:"procs_total"`
	ProcsUID    int            `json:"procs_uid"`
	UID         int            `json:"uid"`
	UIDLimit    int            `json:"uid_limit"`
	TotalLimit  int            `json:"total_limit"`
	Delta       int            `json:"delta"`
	NewPerSec   float64        `json:"new_per_sec"`
	TopParents  []SpawnParent  `json:"top_parents,omitempty"`
	TopCommands []SpawnCommand `json:"top_commands,omitempty"`
}

type spawnTracker struct {
	previous   map[int]struct{}
	at         time.Time
	warnStreak int
	probes     []SpawnState
	argv       map[int]string
}

type spawnPersistItem struct {
	state  SpawnState
	events []Event
	sample bool
}

type spawnPersistRecord struct {
	at     time.Time
	detail string
}

func (t *spawnTracker) update(at time.Time, processes []SpawnProcess, uid, uidLimit, totalLimit int) SpawnState {
	current := make(map[int]struct{}, len(processes))
	s := SpawnState{At: at.UTC(), UID: uid, UIDLimit: uidLimit, TotalLimit: totalLimit}
	for _, p := range processes {
		if p.PID <= 0 {
			continue
		}
		current[p.PID] = struct{}{}
		if p.UID == uid {
			s.ProcsUID++
		}
		if t.previous != nil {
			if _, present := t.previous[p.PID]; !present {
				s.Delta++
			}
		}
	}
	s.ProcsTotal = len(current)
	if t.previous != nil && at.After(t.at) {
		s.NewPerSec = float64(s.Delta) / at.Sub(t.at).Seconds()
	}
	t.previous, t.at = current, at
	t.probes = append(t.probes, s)
	if len(t.probes) > 120 {
		t.probes = t.probes[1:]
	}
	return s
}

func evaluateSpawn(cfg Config, tracker *spawnTracker, state SpawnState) []Condition {
	var out []Condition
	if state.NewPerSec >= cfg.Spawn.RateWarn {
		tracker.warnStreak++
	} else {
		tracker.warnStreak = 0
	}
	if state.NewPerSec >= cfg.Spawn.RateCritical {
		out = append(out, Condition{"spawn.rate", "critical", "Process spawn rate is critical", ""})
	} else if tracker.warnStreak >= 2 {
		out = append(out, Condition{"spawn.rate", "warn", "Process spawn rate is high", ""})
	}
	if severity := spawnPctSeverity(state.ProcsUID, state.UIDLimit, cfg.Spawn.UIDWarnPct, cfg.Spawn.UIDCriticalPct); severity != "" {
		out = append(out, Condition{"procs.uid", severity, "User process limit is near exhaustion", ""})
	}
	if severity := spawnPctSeverity(state.ProcsTotal, state.TotalLimit, cfg.Spawn.TotalWarnPct, cfg.Spawn.TotalCriticalPct); severity != "" {
		out = append(out, Condition{"procs.total", severity, "System process limit is near exhaustion", ""})
	}
	return out
}

func spawnPctSeverity(count, limit int, warn, critical float64) string {
	if limit <= 0 {
		return ""
	}
	pct := 100 * float64(count) / float64(limit)
	if pct >= critical {
		return "critical"
	}
	if pct >= warn {
		return "warn"
	}
	return ""
}

func attributeSpawn(state *SpawnState, processes []SpawnProcess, backend SpawnBackend, readArgv bool) {
	byPID := make(map[int]SpawnProcess, len(processes))
	children := map[int]int{}
	commands := map[string]int{}
	for _, p := range processes {
		byPID[p.PID] = p
		if p.UID == state.UID {
			children[p.PPID]++
			commands[p.Comm]++
		}
	}
	for pid, count := range children {
		if pid > 0 {
			state.TopParents = append(state.TopParents, SpawnParent{PID: pid, Comm: byPID[pid].Comm, ChildCount: count})
		}
	}
	sort.Slice(state.TopParents, func(i, j int) bool {
		a, b := state.TopParents[i], state.TopParents[j]
		return a.ChildCount > b.ChildCount || a.ChildCount == b.ChildCount && a.PID < b.PID
	})
	if len(state.TopParents) > 5 {
		state.TopParents = state.TopParents[:5]
	}
	if readArgv {
		for i := range state.TopParents {
			argv, err := backend.Argv(state.TopParents[i].PID)
			if err == nil {
				state.TopParents[i].Argv = truncateRunes(argv, 200)
			}
		}
	}
	for comm, count := range commands {
		state.TopCommands = append(state.TopCommands, SpawnCommand{comm, count})
	}
	sort.Slice(state.TopCommands, func(i, j int) bool {
		a, b := state.TopCommands[i], state.TopCommands[j]
		return a.Count > b.Count || a.Count == b.Count && a.Comm < b.Comm
	})
	if len(state.TopCommands) > 5 {
		state.TopCommands = state.TopCommands[:5]
	}
}

func (s *Service) spawnHasActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key := range s.engine.active {
		if spawnKey(key) {
			return true
		}
	}
	return false
}

func truncateRunes(value string, maxLen int) string {
	r := []rune(value)
	if len(r) > maxLen {
		return string(r[:maxLen])
	}
	return value
}

func spawnDetail(s SpawnState) string {
	detail := fmt.Sprintf("%.0f new/s; %d procs for uid %d", s.NewPerSec, s.ProcsUID, s.UID)
	if len(s.TopParents) > 0 {
		p := s.TopParents[0]
		detail += fmt.Sprintf("; top parent: %s (pid %d, %d children) %q", p.Comm, p.PID, p.ChildCount, p.Argv)
	}
	if len(s.TopCommands) > 0 {
		parts := make([]string, 0, 2)
		for _, c := range s.TopCommands {
			parts = append(parts, fmt.Sprintf("%s×%d", c.Comm, c.Count))
			if len(parts) == 2 {
				break
			}
		}
		detail += "; top: " + strings.Join(parts, ", ")
	}
	return truncateRunes(detail, 300)
}

func currentUID() int { return os.Getuid() }

func (s *Service) TickSpawn(_ context.Context) error {
	if s.opts.SpawnBackend == nil {
		return nil
	}
	started := s.opts.Clock.Now()
	processes, err := s.opts.SpawnBackend.List()
	if err != nil {
		return fmt.Errorf("spawn process list: %w", err)
	}
	state, conditions := s.sampleSpawn(started, processes)
	events, item, previous := s.publishSpawn(state, conditions)
	for _, event := range events {
		if event.Type == "fired" || event.Type == "updated" && previous[event.Alert.Key] != event.Alert.Severity {
			s.notifySpawn(event.Alert)
		}
	}
	s.enqueueSpawnPersistence(item)
	return nil
}

func (s *Service) sampleSpawn(started time.Time, processes []SpawnProcess) (SpawnState, []Condition) {
	uidLimit, totalLimit := s.opts.SpawnBackend.Limits()
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	state := s.spawnTracker.update(started, processes, s.opts.UID(), uidLimit, totalLimit)
	conditions := evaluateSpawn(s.opts.Config, &s.spawnTracker, state)
	if len(conditions) > 0 || s.spawnHasActive() {
		attributeSpawn(&state, processes, s.opts.SpawnBackend, false)
		s.fillSpawnArgv(&state)
		for i := range conditions {
			conditions[i].Detail = spawnDetail(state)
		}
	}
	s.spawnTracker.probes[len(s.spawnTracker.probes)-1] = state
	return state, conditions
}

func (s *Service) fillSpawnArgv(state *SpawnState) {
	if s.spawnTracker.argv == nil {
		s.spawnTracker.argv = map[int]string{}
	}
	present := make(map[int]bool, len(state.TopParents))
	for _, parent := range state.TopParents {
		present[parent.PID] = true
	}
	for pid := range s.spawnTracker.argv {
		if !present[pid] {
			delete(s.spawnTracker.argv, pid)
		}
	}
	for i := range state.TopParents {
		parent := &state.TopParents[i]
		if _, ok := s.spawnTracker.argv[parent.PID]; !ok {
			argv, err := s.opts.SpawnBackend.Argv(parent.PID)
			if err == nil {
				s.spawnTracker.argv[parent.PID] = truncateRunes(argv, 200)
			}
		}
		parent.Argv = s.spawnTracker.argv[parent.PID]
	}
}

func (s *Service) publishSpawn(state SpawnState, conditions []Condition) ([]Event, spawnPersistItem, map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.Spawn = &state
	previous := make(map[string]string, len(conditions))
	for _, c := range conditions {
		previous[c.Key] = s.engine.active[c.Key].Severity
	}
	parentPID := 0
	if len(state.TopParents) > 0 {
		parentPID = state.TopParents[0].PID
	}
	events := s.engine.EvaluateSpawn(state.At, conditions, parentPID)
	for _, condition := range conditions {
		alert := s.engine.active[condition.Key]
		alert.Spawn = &state
		s.engine.active[condition.Key] = alert
	}
	for i := range events {
		events[i].Alert.Spawn = &state
	}
	item := s.spawnPersistenceItem(state, events, previous)
	s.fanoutLocked(notification{"watch.sample", s.current}, true)
	for _, event := range events {
		s.fanoutLocked(notification{"alerts.event", event}, false)
	}
	return events, item, previous
}

func (s *Service) spawnPersistenceItem(state SpawnState, events []Event, previous map[string]string) spawnPersistItem {
	item := spawnPersistItem{state: state}
	for _, event := range events {
		if event.Type == "resolved" {
			delete(s.spawnPersisted, event.Alert.Key)
			item.events = append(item.events, event)
			continue
		}
		severityChanged := event.Type == "updated" && previous[event.Alert.Key] != event.Alert.Severity
		if !severityChanged && event.Type == "updated" {
			if prev, ok := s.spawnPersisted[event.Alert.Key]; ok && state.At.Sub(prev.at) < 30*time.Second {
				continue
			}
		}
		item.events = append(item.events, event)
		s.spawnPersisted[event.Alert.Key] = spawnPersistRecord{state.At, event.Alert.Detail}
		if event.Type == "fired" || severityChanged {
			item.sample = true
		}
	}
	for _, alert := range s.engine.Active() {
		if !spawnKey(alert.Key) {
			continue
		}
		prev, ok := s.spawnPersisted[alert.Key]
		if ok && (prev.detail == alert.Detail || state.At.Sub(prev.at) < 30*time.Second) {
			continue
		}
		item.events = append(item.events, Event{Type: "detail", Alert: alert})
		s.spawnPersisted[alert.Key] = spawnPersistRecord{state.At, alert.Detail}
	}
	return item
}

func (s *Service) persistSpawn(ctx context.Context, item spawnPersistItem) error {
	if len(item.events) == 0 {
		return nil
	}
	for _, event := range item.events {
		if err := s.opts.Store.UpsertAlert(ctx, toRow(event.Alert)); err != nil {
			s.opts.Logger.Error("spawn alert persistence failed", "error", err)
		}
	}
	if !item.sample {
		return nil
	}
	state := item.state
	sample := Sample{At: state.At, Spawn: &state, Limits: map[string]LimitUsage{"procs_per_uid": usage(state.ProcsUID, state.UIDLimit), "procs": usage(state.ProcsTotal, state.TotalLimit)}}
	data, err := json.Marshal(sample)
	if err != nil {
		return fmt.Errorf("spawn encode sample: %w", err)
	}
	if err := s.opts.Store.SaveHostSample(ctx, state.At, data); err != nil {
		return fmt.Errorf("spawn save sample: %w", err)
	}
	return nil
}

func (s *Service) enqueueSpawnPersistence(item spawnPersistItem) {
	if len(item.events) == 0 {
		return
	}
	s.spawnPersistMu.Lock()
	defer s.spawnPersistMu.Unlock()
	if s.spawnPersistQueue == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.spawnPersistCancel = cancel
		s.spawnPersistQueue = make(chan spawnPersistItem, 16)
		s.spawnPersistDone = make(chan struct{})
		go s.runSpawnPersister(ctx)
	}
	select {
	case s.spawnPersistQueue <- item:
	default:
		s.spawnPersistDropped++
		at := s.opts.Clock.Now()
		if s.spawnPersistWarnAt.IsZero() || at.Sub(s.spawnPersistWarnAt) >= 30*time.Second {
			s.opts.Logger.Warn("spawn persistence queue full", "dropped", s.spawnPersistDropped)
			s.spawnPersistWarnAt = at
		}
	}
}

func (s *Service) runSpawnPersister(ctx context.Context) {
	defer close(s.spawnPersistDone)
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.spawnPersistQueue:
			if err := s.persistSpawn(ctx, item); err != nil {
				s.opts.Logger.Error("spawn sample persistence failed", "error", err)
			}
		}
	}
}

func (s *Service) stopSpawnPersister() {
	s.spawnPersistMu.Lock()
	cancel, done := s.spawnPersistCancel, s.spawnPersistDone
	s.spawnPersistMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	s.waitForSpawn(done)
}

func (s *Service) notifySpawn(alert Alert) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.opts.Notifier.Notify(ctx, alert); err != nil {
			s.opts.Logger.Warn("spawn desktop notification failed", "error", err)
		}
	}()
}

func (s *Service) runSpawn(ctx context.Context) {
	if s.opts.SpawnBackend == nil {
		return
	}
	interval := s.opts.Config.Spawn.Interval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	skip := false
	for {
		if skip {
			skip = false
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			continue
		}
		started := s.opts.Clock.Now()
		cpuBefore, cpuErr := selfCPUTime()
		probeCtx, cancel := context.WithTimeout(ctx, interval)
		if err := s.TickSpawn(probeCtx); err != nil && ctx.Err() == nil {
			s.opts.Logger.Warn("spawn probe failed", "error", err)
		}
		cancel()
		duration := s.opts.Clock.Now().Sub(started)
		if duration > interval {
			skip = true
			if !s.spawnOverrun {
				s.opts.Logger.Warn("spawn probe exceeded interval", "duration", duration)
				s.spawnOverrun = true
			}
		}
		if cpuErr == nil {
			if cpuAfter, err := selfCPUTime(); err == nil {
				s.recordSpawnCPU(started, cpuAfter-cpuBefore)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) recordSpawnCPU(at time.Time, cpu time.Duration) {
	if s.spawnCPUAt.IsZero() {
		s.spawnCPUAt = at
	}
	s.spawnCPU += cpu
	window := at.Sub(s.spawnCPUAt)
	if window < time.Minute {
		return
	}
	pct := 100 * float64(s.spawnCPU) / float64(window)
	if pct > 1 {
		s.opts.Logger.Warn("spawn probe CPU above 1%", "cpu_pct", pct)
	}
	s.spawnCPUAt, s.spawnCPU = at, 0
}

func finiteLimit(v uint64) int {
	if v == ^uint64(0) || v > uint64(^uint(0)>>1) {
		return 0
	}
	return int(v)
}
