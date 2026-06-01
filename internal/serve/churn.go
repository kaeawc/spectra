package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

const churnRetainWindow = 30 * time.Minute

// AppChurnSample captures per-app child process churn for one daemon tick.
type AppChurnSample struct {
	AppPath        string    `json:"app_path"`
	Timestamp      time.Time `json:"timestamp"`
	ChildrenCount  int       `json:"children_count"`
	Spawns1s       int       `json:"spawns_1s"`
	Exits1s        int       `json:"exits_1s"`
	Spawns1m       int       `json:"spawns_1m"`
	Exits1m        int       `json:"exits_1m"`
	FailedSpawns1m int       `json:"failed_spawns_1m"`

	failedSpawns1s int
}

type appChurnAggregate struct {
	MinuteAt     time.Time
	AppPath      string
	Spawns       int
	Exits        int
	FailedSpawns int
}

type processIdentity struct {
	PID       int
	PPID      int
	AppPath   string
	StartTime time.Time
}

type spawnFailureCounter interface {
	CountsSince(cutoff time.Time) map[string]int
}

type churnTracker struct {
	mu       sync.Mutex
	prev     map[int]processIdentity
	history  []AppChurnSample
	failures spawnFailureCounter
	lastTick time.Time
}

func newChurnTracker(failures spawnFailureCounter) *churnTracker {
	return &churnTracker{
		prev:     make(map[int]processIdentity),
		failures: failures,
	}
}

func (c *churnTracker) tick(now time.Time, current []process.Info) []AppChurnSample {
	c.mu.Lock()
	defer c.mu.Unlock()

	currentByPID := make(map[int]processIdentity, len(current))
	childrenByApp := make(map[string]int)
	for _, p := range current {
		id := processIdentity{PID: p.PID, PPID: p.PPID, AppPath: p.AppPath, StartTime: p.StartTime}
		currentByPID[p.PID] = id
		if p.AppPath != "" {
			childrenByApp[p.AppPath]++
		}
	}

	spawnsByApp := make(map[string]int)
	exitsByApp := make(map[string]int)
	for pid, cur := range currentByPID {
		prev, existed := c.prev[pid]
		if !existed {
			incrementApp(spawnsByApp, cur.AppPath)
			continue
		}
		if processStartChanged(prev.StartTime, cur.StartTime) {
			incrementApp(exitsByApp, prev.AppPath)
			incrementApp(spawnsByApp, cur.AppPath)
		}
	}
	for pid, prev := range c.prev {
		if _, still := currentByPID[pid]; !still {
			incrementApp(exitsByApp, prev.AppPath)
		}
	}

	oneMinuteAgo := now.Add(-time.Minute)
	failed1m := c.failureCounts(oneMinuteAgo)
	failed1s := map[string]int{}
	if !c.lastTick.IsZero() {
		failed1s = c.failureCounts(c.lastTick)
	}

	apps := unionApps(childrenByApp, spawnsByApp, exitsByApp, failed1m)
	samples := make([]AppChurnSample, 0, len(apps))
	for _, app := range apps {
		s := AppChurnSample{
			AppPath:        app,
			Timestamp:      now,
			ChildrenCount:  childrenByApp[app],
			Spawns1s:       spawnsByApp[app],
			Exits1s:        exitsByApp[app],
			Spawns1m:       spawnsByApp[app],
			Exits1m:        exitsByApp[app],
			FailedSpawns1m: failed1m[app],
			failedSpawns1s: failed1s[app],
		}
		samples = append(samples, s)
	}

	c.history = append(c.history, samples...)
	c.trimLocked(now.Add(-churnRetainWindow))
	c.applyRollingLocked(oneMinuteAgo, samples)
	c.prev = currentByPID
	c.lastTick = now
	return append([]AppChurnSample(nil), samples...)
}

func (c *churnTracker) current(limit int) []AppChurnSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	latest := make(map[string]AppChurnSample)
	for i := len(c.history) - 1; i >= 0; i-- {
		s := c.history[i]
		if _, seen := latest[s.AppPath]; !seen {
			latest[s.AppPath] = s
		}
	}
	out := make([]AppChurnSample, 0, len(latest))
	for _, s := range latest {
		out = append(out, s)
	}
	sortChurnSamples(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (c *churnTracker) recent(appPath string, limit int) []AppChurnSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []AppChurnSample
	for _, s := range c.history {
		if appPath == "" || s.AppPath == appPath {
			out = append(out, s)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return append([]AppChurnSample(nil), out...)
}

func (c *churnTracker) flushAggregates(retainWindow time.Duration) []appChurnAggregate {
	c.mu.Lock()
	defer c.mu.Unlock()
	if retainWindow <= 0 {
		retainWindow = churnRetainWindow
	}
	cutoff := time.Now().UTC().Add(-retainWindow)
	c.trimLocked(cutoff)
	type key struct {
		app    string
		minute time.Time
	}
	groups := make(map[key]*appChurnAggregate)
	for _, s := range c.history {
		k := key{app: s.AppPath, minute: s.Timestamp.UTC().Truncate(time.Minute)}
		agg := groups[k]
		if agg == nil {
			agg = &appChurnAggregate{MinuteAt: k.minute, AppPath: k.app}
			groups[k] = agg
		}
		agg.Spawns += s.Spawns1s
		agg.Exits += s.Exits1s
		agg.FailedSpawns += s.failedSpawns1s
	}
	out := make([]appChurnAggregate, 0, len(groups))
	for _, agg := range groups {
		out = append(out, *agg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MinuteAt.Equal(out[j].MinuteAt) {
			return out[i].AppPath < out[j].AppPath
		}
		return out[i].MinuteAt.Before(out[j].MinuteAt)
	})
	return out
}

func (c *churnTracker) applyRollingLocked(cutoff time.Time, samples []AppChurnSample) {
	for i := range samples {
		app := samples[i].AppPath
		for _, prev := range c.history {
			if prev.AppPath != app || prev.Timestamp.Before(cutoff) || prev.Timestamp.Equal(samples[i].Timestamp) {
				continue
			}
			samples[i].Spawns1m += prev.Spawns1s
			samples[i].Exits1m += prev.Exits1s
		}
	}
	start := len(c.history) - len(samples)
	for i, s := range samples {
		c.history[start+i] = s
	}
}

func (c *churnTracker) trimLocked(cutoff time.Time) {
	keepFrom := 0
	for keepFrom < len(c.history) && c.history[keepFrom].Timestamp.Before(cutoff) {
		keepFrom++
	}
	if keepFrom > 0 {
		copy(c.history, c.history[keepFrom:])
		c.history = c.history[:len(c.history)-keepFrom]
	}
}

func (c *churnTracker) failureCounts(cutoff time.Time) map[string]int {
	if c.failures == nil {
		return nil
	}
	return c.failures.CountsSince(cutoff)
}

func processStartChanged(prev, cur time.Time) bool {
	if prev.IsZero() || cur.IsZero() {
		return false
	}
	return !prev.Equal(cur)
}

func incrementApp(m map[string]int, appPath string) {
	if appPath != "" {
		m[appPath]++
	}
}

func unionApps(maps ...map[string]int) []string {
	set := make(map[string]struct{})
	for _, m := range maps {
		for app := range m {
			if app != "" {
				set[app] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for app := range set {
		out = append(out, app)
	}
	sort.Strings(out)
	return out
}

func sortChurnSamples(samples []AppChurnSample) {
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Spawns1m != samples[j].Spawns1m {
			return samples[i].Spawns1m > samples[j].Spawns1m
		}
		if samples[i].Exits1m != samples[j].Exits1m {
			return samples[i].Exits1m > samples[j].Exits1m
		}
		if samples[i].FailedSpawns1m != samples[j].FailedSpawns1m {
			return samples[i].FailedSpawns1m > samples[j].FailedSpawns1m
		}
		return samples[i].AppPath < samples[j].AppPath
	})
}

type spawnFailureEvent struct {
	AppPath string
	At      time.Time
}

type spawnFailureWatcher struct {
	mu     sync.Mutex
	events []spawnFailureEvent
	run    func(context.Context, func(...spawnFailureEvent)) error
}

func newSpawnFailureWatcher() *spawnFailureWatcher {
	w := &spawnFailureWatcher{}
	w.run = w.runLogStream
	return w
}

func (w *spawnFailureWatcher) Start(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := w.run(ctx, w.add)
		if err == nil {
			backoff = time.Second
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
		}
	}
}

func (w *spawnFailureWatcher) CountsSince(cutoff time.Time) map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trimLocked(cutoff.Add(-time.Minute))
	out := make(map[string]int)
	for _, event := range w.events {
		if !event.At.Before(cutoff) && event.AppPath != "" {
			out[event.AppPath]++
		}
	}
	return out
}

func (w *spawnFailureWatcher) add(events ...spawnFailureEvent) {
	w.mu.Lock()
	w.events = append(w.events, events...)
	w.trimLocked(time.Now().UTC().Add(-churnRetainWindow))
	w.mu.Unlock()
}

func (w *spawnFailureWatcher) trimLocked(cutoff time.Time) {
	keepFrom := 0
	for keepFrom < len(w.events) && w.events[keepFrom].At.Before(cutoff) {
		keepFrom++
	}
	if keepFrom > 0 {
		copy(w.events, w.events[keepFrom:])
		w.events = w.events[:len(w.events)-keepFrom]
	}
}

func (w *spawnFailureWatcher) runLogStream(ctx context.Context, emit func(...spawnFailureEvent)) error {
	cmd := exec.CommandContext(ctx, "log", "stream",
		"--predicate", `eventMessage CONTAINS "posix_spawn" AND eventMessage CONTAINS "failed"`,
		"--info", "--style", "ndjson")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if event, ok := parseSpawnFailureLogLine(scanner.Bytes(), time.Now().UTC()); ok {
			emit(event)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Wait()
		return scanErr
	}
	return cmd.Wait()
}

func parseSpawnFailureLogLine(raw []byte, fallback time.Time) (spawnFailureEvent, bool) {
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		return spawnFailureEvent{}, false
	}
	app := appPathFromLogEntry(entry)
	if app == "" {
		return spawnFailureEvent{}, false
	}
	at := fallback
	for _, key := range []string{"timestamp", "date", "machTimestamp"} {
		if v, ok := entry[key].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
				at = parsed.UTC()
				break
			}
		}
	}
	return spawnFailureEvent{AppPath: app, At: at}, true
}

func appPathFromLogEntry(entry map[string]any) string {
	for _, key := range []string{"processImagePath", "senderImagePath", "process", "sender", "eventMessage"} {
		if v, ok := entry[key].(string); ok {
			if app := extractAppPath(v); app != "" {
				return app
			}
		}
	}
	return ""
}

func extractAppPath(raw string) string {
	idx := strings.Index(raw, ".app")
	if idx < 0 {
		return ""
	}
	end := idx + len(".app")
	start := strings.LastIndex(raw[:end], "/")
	for start > 0 {
		prefix := raw[start:end]
		if strings.HasPrefix(prefix, "/Applications/") || strings.HasPrefix(prefix, "/System/Applications/") {
			return prefix
		}
		next := strings.LastIndex(raw[:start], "/")
		if next < 0 {
			break
		}
		start = next
	}
	return raw[:end]
}

func churnLoop(ctx context.Context, tracker *churnTracker, interval time.Duration, collect func(context.Context) []process.Info) {
	if interval <= 0 {
		interval = time.Second
	}
	if collect == nil {
		cache := &appPathCache{refreshEvery: 5 * time.Minute}
		collect = func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{BundlePaths: cache.paths(time.Now())})
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			tracker.tick(now.UTC(), collect(ctx))
		}
	}
}

type appPathCache struct {
	mu           sync.Mutex
	refreshEvery time.Duration
	nextRefresh  time.Time
	appPaths     []string
}

func (c *appPathCache) paths(now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appPaths != nil && now.Before(c.nextRefresh) {
		return append([]string(nil), c.appPaths...)
	}
	c.appPaths = scanInstalledAppPaths()
	if c.refreshEvery <= 0 {
		c.refreshEvery = 5 * time.Minute
	}
	c.nextRefresh = now.Add(c.refreshEvery)
	return append([]string(nil), c.appPaths...)
}

func scanInstalledAppPaths() []string {
	var paths []string
	for _, root := range []string{"/Applications", "/Applications/Utilities"} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && strings.HasSuffix(entry.Name(), ".app") {
				paths = append(paths, filepath.Join(root, entry.Name()))
			}
		}
	}
	sort.Strings(paths)
	return paths
}
