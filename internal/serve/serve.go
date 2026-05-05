// Package serve wires up the Spectra daemon: it creates the Unix socket
// listener, registers JSON-RPC 2.0 handlers for every public method, and
// runs the accept loop until the context is cancelled.
//
// See docs/operations/daemon.md for the design.
package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/diff"
	"github.com/kaeawc/spectra/internal/helperclient"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/toolchain"
)

var (
	collectToolchains = toolchain.Collect
	collectJDKs       = toolchain.CollectJDKs
	runJFRDump        = jvm.JFRDump
)

// DefaultSockPath returns the canonical Unix socket path (~/.spectra/sock).
func DefaultSockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".spectra", "sock"), nil
}

// Options configures the daemon.
type Options struct {
	SockPath       string
	TCPAddr        string
	SpectraVersion string
	DBPath         string              // empty = store.DefaultPath()
	CacheRegistry  *cache.Registry     // nil = cache.Default
	DetectStore    *cache.ShardedStore // nil = no detect caching
}

// Run starts the daemon: listens on the Unix socket and serves requests until
// ctx is cancelled. Run blocks until the listener is shut down.
func Run(ctx context.Context, opts Options) error {
	sockPath := opts.SockPath
	if sockPath == "" {
		var err error
		sockPath, err = DefaultSockPath()
		if err != nil {
			return fmt.Errorf("serve: resolve sock path: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return fmt.Errorf("serve: mkdir: %w", err)
	}
	// Remove stale socket file if daemon was not cleanly shut down.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("serve: listen %s: %w", sockPath, err)
	}
	// Restrict access to the running user.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("serve: chmod %s: %w", sockPath, err)
	}

	listeners := []net.Listener{ln}
	if opts.TCPAddr != "" {
		tcpLn, err := net.Listen("tcp", opts.TCPAddr)
		if err != nil {
			ln.Close()
			os.Remove(sockPath)
			return fmt.Errorf("serve: listen tcp %s: %w", opts.TCPAddr, err)
		}
		listeners = append(listeners, tcpLn)
	}
	defer os.Remove(sockPath)
	defer closeListeners(listeners)

	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath, err = store.DefaultPath()
		if err != nil {
			return fmt.Errorf("serve: resolve db path: %w", err)
		}
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("serve: open db: %w", err)
	}
	defer db.Close()

	// Start the ~1Hz process metrics sampler.
	collector := metrics.NewCollector()
	sampler := metrics.NewSampler(collector, time.Second, nil)
	go sampler.Run(ctx)

	// Flush aggregates to SQLite every minute.
	go flushMetricsLoop(ctx, collector, db)

	// Capture a live snapshot every minute; prune to the last 100 live snapshots.
	go snapshotLoop(ctx, opts.SpectraVersion, db)

	cacheReg := opts.CacheRegistry
	if cacheReg == nil {
		cacheReg = cache.Default
	}

	d := rpc.NewDispatcher()
	registerHandlers(d, opts.SpectraVersion, db, collector, cacheReg, opts.DetectStore)

	// Close the listener when ctx is cancelled to unblock Accept.
	go func() {
		<-ctx.Done()
		closeListeners(listeners)
	}()

	return serveAll(d, listeners)
}

func serveAll(d *rpc.Dispatcher, listeners []net.Listener) error {
	errCh := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func(ln net.Listener) {
			errCh <- d.ServeListener(ln)
		}(ln)
	}
	for range listeners {
		if err := <-errCh; err != nil {
			closeListeners(listeners)
			return err
		}
	}
	return nil
}

func closeListeners(listeners []net.Listener) {
	for _, ln := range listeners {
		_ = ln.Close()
	}
}

// flushMetricsLoop writes 1-minute aggregates from the ring buffer to SQLite
// on a 1-minute tick until ctx is cancelled.
func flushMetricsLoop(ctx context.Context, c *metrics.Collector, db *store.DB) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			aggs := c.FlushAggregates(5 * time.Minute)
			if len(aggs) == 0 {
				continue
			}
			rows := make([]store.ProcessMetricRow, len(aggs))
			for i, a := range aggs {
				rows[i] = store.ProcessMetricRow{
					PID:         a.PID,
					MinuteAt:    a.MinuteAt,
					AvgRSSKiB:   a.AvgRSSKiB,
					MaxRSSKiB:   a.MaxRSSKiB,
					AvgCPUPct:   a.AvgCPUPct,
					MaxCPUPct:   a.MaxCPUPct,
					SampleCount: a.SampleCount,
				}
			}
			_ = db.SaveProcessMetrics(ctx, rows)
		}
	}
}

// snapshotLoop captures a live host-only snapshot every minute and prunes the
// live snapshot history to the last 100 entries. Apps, storage, and JVMs are
// skipped to keep the per-tick cost low (~50ms vs seconds for a full snapshot).
func snapshotLoop(ctx context.Context, version string, db *store.DB) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := snapshot.Build(ctx, snapshot.Options{
				SpectraVersion: version,
				SkipApps:       true,
				SkipStorage:    true,
				SkipJVMs:       true,
			})
			_ = db.SaveSnapshot(ctx, store.FromSnapshot(snap))
			_ = db.SaveSnapshotProcesses(ctx, snap.ID, store.ProcessesFromSnapshot(snap))
			_ = db.SaveLoginItems(ctx, snap.ID, store.LoginItemsFromSnapshot(snap))
			_ = db.SaveGrantedPerms(ctx, snap.ID, store.GrantedPermsFromSnapshot(snap))
			_, _ = db.PruneSnapshots(ctx, 100)
		}
	}
}

// registerHandlers wires all JSON-RPC methods into d.
//
//gocyclo:ignore
func registerHandlers(d *rpc.Dispatcher, version string, db *store.DB, collector *metrics.Collector, cacheReg *cache.Registry, detectStore *cache.ShardedStore) {
	d.Register("health", func(_ json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "version": version}, nil
	})

	d.Register("snapshot.list", func(_ json.RawMessage) (any, error) {
		return db.ListSnapshots(context.Background(), "")
	})

	d.Register("snapshot.get", func(params json.RawMessage) (any, error) {
		var p struct{ ID string }
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, fmt.Errorf("snapshot.get requires {\"ID\":\"<id>\"}")
		}
		raw, err := db.GetSnapshotJSON(context.Background(), p.ID)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			return nil, fmt.Errorf("snapshot %q has no JSON blob", p.ID)
		}
		var s snapshot.Snapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return s, nil
	})

	d.Register("snapshot.create", func(_ json.RawMessage) (any, error) {
		snap := snapshot.Build(context.Background(), snapshot.Options{
			SpectraVersion: version,
			DetectOpts:     detect.Options{},
			DetectStore:    detectStore,
		})
		if err := db.SaveSnapshot(context.Background(), store.FromSnapshot(snap)); err != nil {
			return nil, err
		}
		_ = db.SaveSnapshotProcesses(context.Background(), snap.ID, store.ProcessesFromSnapshot(snap))
		_ = db.SaveLoginItems(context.Background(), snap.ID, store.LoginItemsFromSnapshot(snap))
		_ = db.SaveGrantedPerms(context.Background(), snap.ID, store.GrantedPermsFromSnapshot(snap))
		return snap, nil
	})

	d.Register("process.live", func(params json.RawMessage) (any, error) {
		// Returns the most recent samples for all known PIDs.
		var p struct {
			Limit int `json:"limit"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Limit <= 0 {
			p.Limit = 60 // default: last 60 seconds
		}
		pids := collector.PIDs()
		result := make(map[string]any, len(pids))
		for _, pid := range pids {
			samples := collector.Recent(pid, p.Limit)
			if len(samples) > 0 {
				result[fmt.Sprint(pid)] = samples
			}
		}
		return result, nil
	})

	d.Register("process.history", func(params json.RawMessage) (any, error) {
		var p struct {
			PID   int `json:"pid"`
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("process.history requires {\"pid\": <pid>}")
		}
		return db.GetProcessMetrics(context.Background(), p.PID, p.Limit)
	})

	d.Register("inspect.app", func(params json.RawMessage) (any, error) {
		var p struct {
			Path    string         `json:"path"`
			Options detect.Options `json:"options"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
			return nil, fmt.Errorf("inspect.app requires {\"path\": \"<app-path>\"}")
		}
		return detect.DetectWith(p.Path, p.Options)
	})

	d.Register("inspect.app.batch", func(params json.RawMessage) (any, error) {
		var p struct {
			Paths   []string       `json:"paths"`
			Options detect.Options `json:"options"`
		}
		if err := json.Unmarshal(params, &p); err != nil || len(p.Paths) == 0 {
			return nil, fmt.Errorf("inspect.app.batch requires {\"paths\": [...]}")
		}
		results := make([]detect.Result, 0, len(p.Paths))
		for _, path := range p.Paths {
			r, err := detect.DetectWith(path, p.Options)
			if err != nil {
				continue // silently skip unreadable bundles
			}
			results = append(results, r)
		}
		return results, nil
	})

	d.Register("inspect.host", func(_ json.RawMessage) (any, error) {
		return snapshot.CollectHost(version), nil
	})

	d.Register("snapshot.diff", func(params json.RawMessage) (any, error) {
		var p struct {
			IDA string `json:"id_a"`
			IDB string `json:"id_b"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.IDA == "" || p.IDB == "" {
			return nil, fmt.Errorf("snapshot.diff requires {\"id_a\": \"...\", \"id_b\": \"...\"}")
		}
		ctx := context.Background()
		loadSnap := func(id string) (*snapshot.Snapshot, error) {
			raw, err := db.GetSnapshotJSON(ctx, id)
			if err != nil {
				return nil, err
			}
			if raw == nil {
				return nil, fmt.Errorf("snapshot %q has no JSON blob", id)
			}
			var s snapshot.Snapshot
			return &s, json.Unmarshal(raw, &s)
		}
		snapA, err := loadSnap(p.IDA)
		if err != nil {
			return nil, fmt.Errorf("snapshot %q: %w", p.IDA, err)
		}
		snapB, err := loadSnap(p.IDB)
		if err != nil {
			return nil, fmt.Errorf("snapshot %q: %w", p.IDB, err)
		}
		return diff.Compare(*snapA, *snapB), nil
	})

	d.Register("snapshot.processes", func(params json.RawMessage) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, fmt.Errorf("snapshot.processes requires {\"id\":\"<snapshot-id>\"}")
		}
		return db.GetSnapshotProcesses(context.Background(), p.ID)
	})

	d.Register("snapshot.login_items", func(params json.RawMessage) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, fmt.Errorf("snapshot.login_items requires {\"id\":\"<snapshot-id>\"}")
		}
		return db.GetLoginItems(context.Background(), p.ID)
	})

	d.Register("snapshot.granted_perms", func(params json.RawMessage) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, fmt.Errorf("snapshot.granted_perms requires {\"id\":\"<snapshot-id>\"}")
		}
		return db.GetGrantedPerms(context.Background(), p.ID)
	})

	// snapshot.prune — delete live snapshots beyond retention limit.
	// Optional: { "keep": 100 }
	d.Register("snapshot.prune", func(params json.RawMessage) (any, error) {
		var p struct {
			Keep int `json:"keep"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Keep <= 0 {
			p.Keep = 100
		}
		deleted, err := db.PruneSnapshots(context.Background(), p.Keep)
		if err != nil {
			return nil, err
		}
		return map[string]any{"deleted": deleted, "keep": p.Keep}, nil
	})

	d.Register("rules.check", func(params json.RawMessage) (any, error) {
		// Optional: { "snapshot_id": "snap-..." } to evaluate against stored snapshot.
		var p struct {
			SnapshotID string `json:"snapshot_id"`
		}
		_ = json.Unmarshal(params, &p)

		var snap snapshot.Snapshot
		if p.SnapshotID != "" {
			raw, err := db.GetSnapshotJSON(context.Background(), p.SnapshotID)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &snap); err != nil {
				return nil, err
			}
		} else {
			snap = snapshot.Build(context.Background(), snapshot.Options{
				SpectraVersion: version,
				DetectOpts:     detect.Options{},
				DetectStore:    detectStore,
			})
		}
		return rules.Evaluate(snap, rules.V1Catalog()), nil
	})

	// issues.list — { "machine_uuid": "...", "status": "open" }
	// status is optional; omit to return all statuses.
	d.Register("issues.list", func(params json.RawMessage) (any, error) {
		var p struct {
			MachineUUID string `json:"machine_uuid"`
			Status      string `json:"status"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.MachineUUID == "" {
			return nil, fmt.Errorf("issues.list requires {\"machine_uuid\": \"...\"}")
		}
		return db.ListIssues(context.Background(), p.MachineUUID, store.IssueStatus(p.Status))
	})

	// issues.record — persist findings from a snapshot evaluation into the issues table.
	// { "machine_uuid": "...", "snapshot_id": "...", "findings": [...] }
	d.Register("issues.record", func(params json.RawMessage) (any, error) {
		var p struct {
			MachineUUID string               `json:"machine_uuid"`
			SnapshotID  string               `json:"snapshot_id"`
			Findings    []store.FindingInput `json:"findings"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.MachineUUID == "" || p.SnapshotID == "" {
			return nil, fmt.Errorf("issues.record requires {\"machine_uuid\": \"...\", \"snapshot_id\": \"...\", \"findings\": [...]}")
		}
		ids, err := db.UpsertIssues(context.Background(), p.MachineUUID, p.SnapshotID, p.Findings)
		if err != nil {
			return nil, err
		}
		return map[string]any{"upserted": len(ids), "ids": ids}, nil
	})

	// issues.update — change an issue's status.
	// { "id": "...", "status": "acknowledged" }
	d.Register("issues.update", func(params json.RawMessage) (any, error) {
		var p struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" || p.Status == "" {
			return nil, fmt.Errorf("issues.update requires {\"id\": \"...\", \"status\": \"...\"}")
		}
		if err := db.UpdateIssueStatus(context.Background(), p.ID, store.IssueStatus(p.Status)); err != nil {
			return nil, err
		}
		return map[string]any{"id": p.ID, "status": p.Status}, nil
	})

	// issues.acknowledge — shorthand for issues.update with status=acknowledged.
	// { "id": "..." }
	d.Register("issues.acknowledge", func(params json.RawMessage) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, fmt.Errorf("issues.acknowledge requires {\"id\": \"...\"}")
		}
		if err := db.UpdateIssueStatus(context.Background(), p.ID, store.IssueAcknowledged); err != nil {
			return nil, err
		}
		return map[string]any{"id": p.ID, "status": "acknowledged"}, nil
	})

	// issues.dismiss — shorthand for issues.update with status=dismissed.
	// { "id": "..." }
	d.Register("issues.dismiss", func(params json.RawMessage) (any, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ID == "" {
			return nil, fmt.Errorf("issues.dismiss requires {\"id\": \"...\"}")
		}
		if err := db.UpdateIssueStatus(context.Background(), p.ID, store.IssueDismissed); err != nil {
			return nil, err
		}
		return map[string]any{"id": p.ID, "status": "dismissed"}, nil
	})

	// issues.fix.record — log a fix attempt against an issue.
	// { "issue_id": "...", "applied_by": "user", "command": "...", "output": "...", "exit_code": 0 }
	d.Register("issues.fix.record", func(params json.RawMessage) (any, error) {
		var p store.AppliedFixInput
		if err := json.Unmarshal(params, &p); err != nil || p.IssueID == "" {
			return nil, fmt.Errorf("issues.fix.record requires {\"issue_id\": \"...\"}")
		}
		id, err := db.RecordAppliedFix(context.Background(), p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id}, nil
	})

	// issues.fix.list — list fix attempts for one issue.
	// { "issue_id": "..." }
	d.Register("issues.fix.list", func(params json.RawMessage) (any, error) {
		var p struct {
			IssueID string `json:"issue_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.IssueID == "" {
			return nil, fmt.Errorf("issues.fix.list requires {\"issue_id\": \"...\"}")
		}
		return db.ListAppliedFixes(context.Background(), p.IssueID)
	})

	// jvm.list — list all running JVM processes.
	// Optional: { "pid": 1234 } to inspect a single PID.
	d.Register("jvm.list", func(params json.RawMessage) (any, error) {
		var p struct {
			PID int `json:"pid"`
		}
		_ = json.Unmarshal(params, &p)
		jvmOpts := daemonJVMOptions()
		if p.PID != 0 {
			info := jvm.InspectPID(context.Background(), p.PID, jvmOpts)
			if info == nil {
				return nil, fmt.Errorf("JVM PID %d not found", p.PID)
			}
			return info, nil
		}
		return jvm.CollectAll(context.Background(), jvmOpts), nil
	})

	// jvm.thread_dump — run `jcmd <pid> Thread.print` and return the raw text.
	jvmThreadDump := func(params json.RawMessage) (any, error) {
		var p struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.thread_dump requires {\"pid\": <pid>}")
		}
		out, err := jvm.ThreadDump(p.PID, nil)
		if err != nil {
			return nil, fmt.Errorf("thread dump pid %d: %w", p.PID, err)
		}
		return map[string]any{"pid": p.PID, "output": string(out)}, nil
	}
	d.Register("jvm.thread_dump", jvmThreadDump)
	d.Register("jvm.threadDump", jvmThreadDump)

	// jvm.heap_histogram — run `jcmd <pid> GC.class_histogram` and return the raw text.
	jvmHeapHistogram := func(params json.RawMessage) (any, error) {
		var p struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.heap_histogram requires {\"pid\": <pid>}")
		}
		out, err := jvm.HeapHistogram(p.PID, nil)
		if err != nil {
			return nil, fmt.Errorf("heap histogram pid %d: %w", p.PID, err)
		}
		return map[string]any{"pid": p.PID, "output": string(out)}, nil
	}
	d.Register("jvm.heap_histogram", jvmHeapHistogram)
	d.Register("jvm.heapHistogram", jvmHeapHistogram)

	// jvm.gc_stats — run `jstat -gc <pid>` and return parsed GC counters.
	jvmGCStats := func(params json.RawMessage) (any, error) {
		var p struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.gc_stats requires {\"pid\": <pid>}")
		}
		stats, err := jvm.CollectGCStats(p.PID, nil)
		if err != nil {
			return nil, fmt.Errorf("gc stats pid %d: %w", p.PID, err)
		}
		return stats, nil
	}
	d.Register("jvm.gc_stats", jvmGCStats)
	d.Register("jvm.gcStats", jvmGCStats)

	// jvm.jfr.start — start a JFR recording on a JVM process.
	// Required: {"pid": <pid>}. Optional: {"name": "spectra"}.
	d.Register("jvm.jfr.start", func(params json.RawMessage) (any, error) {
		var p struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.jfr.start requires {\"pid\": <pid>}")
		}
		name := p.Name
		if name == "" {
			name = "spectra"
		}
		if err := jvm.JFRStart(p.PID, name, nil); err != nil {
			return nil, fmt.Errorf("jfr start pid %d: %w", p.PID, err)
		}
		return map[string]any{"pid": p.PID, "name": name, "started": true}, nil
	})

	// jvm.jfr.dump — dump a running JFR recording. Required: {"pid": <pid>, "dest": "/path/to/out.jfr"}.
	d.Register("jvm.jfr.dump", func(params json.RawMessage) (any, error) {
		var p struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
			Dest string `json:"dest"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.jfr.dump requires {\"pid\": <pid>, \"dest\": \"...\"}")
		}
		if p.Dest == "" {
			return nil, fmt.Errorf("jvm.jfr.dump requires {\"dest\": \"...\"}")
		}
		name := p.Name
		if name == "" {
			name = "spectra"
		}
		if err := runJFRDump(p.PID, name, p.Dest, nil); err != nil {
			return nil, fmt.Errorf("jfr dump pid %d: %w", p.PID, err)
		}
		return map[string]any{"pid": p.PID, "name": name, "dest": p.Dest, "dumped": true}, nil
	})

	// jvm.jfr.stop — stop a JFR recording. Optional: {"dest": "/path/to/out.jfr"}.
	d.Register("jvm.jfr.stop", func(params json.RawMessage) (any, error) {
		var p struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
			Dest string `json:"dest"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.jfr.stop requires {\"pid\": <pid>}")
		}
		name := p.Name
		if name == "" {
			name = "spectra"
		}
		if err := jvm.JFRStop(p.PID, name, p.Dest, nil); err != nil {
			return nil, fmt.Errorf("jfr stop pid %d: %w", p.PID, err)
		}
		return map[string]any{"pid": p.PID, "name": name, "dest": p.Dest, "stopped": true}, nil
	})

	// jvm.inspect — structured inspection of one JVM process.
	// Required: {"pid": <pid>}
	d.Register("jvm.inspect", func(params json.RawMessage) (any, error) {
		var p struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.inspect requires {\"pid\": <pid>}")
		}
		info := jvm.InspectPID(context.Background(), p.PID, daemonJVMOptions())
		if info == nil {
			return nil, fmt.Errorf("JVM PID %d not found or not a Java process", p.PID)
		}
		return info, nil
	})

	// jvm.heap_dump — trigger jcmd GC.heap_dump and return the destination path.
	// Required: {"pid": <pid>}. Optional: {"dest": "/path/to/out.hprof"}.
	jvmHeapDump := func(params json.RawMessage) (any, error) {
		var p struct {
			PID  int    `json:"pid"`
			Dest string `json:"dest"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("jvm.heap_dump requires {\"pid\": <pid>}")
		}
		if p.Dest == "" {
			p.Dest = fmt.Sprintf("/tmp/spectra-heap-%d.hprof", p.PID)
		}
		if err := jvm.HeapDump(p.PID, p.Dest, nil); err != nil {
			return nil, fmt.Errorf("heap dump pid %d: %w", p.PID, err)
		}
		return map[string]any{"pid": p.PID, "dest": p.Dest}, nil
	}
	d.Register("jvm.heap_dump", jvmHeapDump)
	d.Register("jvm.heapDump", jvmHeapDump)

	// jdk.list — enumerate installed JDK toolchains.
	d.Register("jdk.list", func(_ json.RawMessage) (any, error) {
		tc := collectToolchains(context.Background(), toolchain.CollectOptions{})
		return tc.JDKs, nil
	})

	// toolchain.scan — full toolchain inventory (brew, JDKs, Node, Python, Go, etc.).
	d.Register("toolchain.scan", func(_ json.RawMessage) (any, error) {
		return collectToolchains(context.Background(), toolchain.CollectOptions{}), nil
	})

	// toolchain.brew — Homebrew formulae, casks, and taps.
	d.Register("toolchain.brew", func(_ json.RawMessage) (any, error) {
		tc := collectToolchains(context.Background(), toolchain.CollectOptions{})
		return map[string]any{
			"formulae": tc.Brew.Formulae,
			"casks":    tc.Brew.Casks,
			"taps":     tc.Brew.Taps,
		}, nil
	})

	// toolchain.runtimes — language runtime installs (Node, Python, Go, Ruby, Rust).
	d.Register("toolchain.runtimes", func(_ json.RawMessage) (any, error) {
		tc := collectToolchains(context.Background(), toolchain.CollectOptions{})
		return map[string]any{
			"node":   tc.Node,
			"python": tc.Python,
			"go":     tc.Go,
			"ruby":   tc.Ruby,
			"rust":   tc.Rust,
		}, nil
	})

	// toolchain.build_tools — installed JVM-ecosystem build tools (Maven, Gradle, Bazel, Make, CMake).
	d.Register("toolchain.build_tools", func(_ json.RawMessage) (any, error) {
		tc := collectToolchains(context.Background(), toolchain.CollectOptions{})
		return tc.BuildTools, nil
	})

	// jdk.scan — re-enumerate installed JDK toolchains (alias for jdk.list that signals a fresh scan intent).
	d.Register("jdk.scan", func(_ json.RawMessage) (any, error) {
		tc := collectToolchains(context.Background(), toolchain.CollectOptions{})
		return tc.JDKs, nil
	})

	// network.state — current network configuration snapshot.
	d.Register("network.state", func(_ json.RawMessage) (any, error) {
		return netstate.Collect(netstate.DefaultRunner), nil
	})

	// network.connections — active TCP/UDP sockets (non-LISTEN).
	d.Register("network.connections", func(_ json.RawMessage) (any, error) {
		return netstate.CollectConnections(netstate.DefaultRunner), nil
	})

	// network.byApp — active connections grouped by app bundle path.
	// Runs CollectConnections + CollectAll and joins on PID.
	// Optional: {"bundles": ["/Applications/Foo.app"]} to scope app attribution.
	d.Register("network.byApp", func(params json.RawMessage) (any, error) {
		var p struct {
			Bundles []string `json:"bundles"`
		}
		_ = json.Unmarshal(params, &p)

		conns := netstate.CollectConnections(netstate.DefaultRunner)
		procs := process.CollectAll(context.Background(), process.CollectOptions{
			BundlePaths: p.Bundles,
		})

		// Build PID → AppPath map.
		pidApp := make(map[int]string, len(procs))
		for _, pr := range procs {
			if pr.AppPath != "" {
				pidApp[pr.PID] = pr.AppPath
			}
		}

		// Group connections by AppPath ("" for unattributed).
		type connWithApp struct {
			netstate.Connection
			AppPath string `json:"app_path,omitempty"`
		}
		grouped := make(map[string][]connWithApp)
		for _, c := range conns {
			app := pidApp[c.PID]
			grouped[app] = append(grouped[app], connWithApp{Connection: c, AppPath: app})
		}
		return grouped, nil
	})

	// process.list — snapshot of all running processes via ps.
	// Optional: { "bundles": ["/Applications/Foo.app"], "deep": true }
	d.Register("process.list", func(params json.RawMessage) (any, error) {
		var p struct {
			Bundles []string `json:"bundles"`
			Deep    bool     `json:"deep"`
		}
		_ = json.Unmarshal(params, &p)
		return process.CollectAll(context.Background(), process.CollectOptions{
			BundlePaths: p.Bundles,
			Deep:        p.Deep,
		}), nil
	})

	// process.tree — process list arranged as a parent-child tree.
	d.Register("process.tree", func(params json.RawMessage) (any, error) {
		var p struct {
			Bundles []string `json:"bundles"`
		}
		_ = json.Unmarshal(params, &p)
		procs := process.CollectAll(context.Background(), process.CollectOptions{
			BundlePaths: p.Bundles,
		})
		return process.BuildTree(procs), nil
	})

	// process.sample — run `sample <pid> <duration>` and return the text output.
	// Required: {"pid": <pid>}. Optional: {"duration": 1, "interval": 10}.
	d.Register("process.sample", func(params json.RawMessage) (any, error) {
		var p struct {
			PID      int `json:"pid"`
			Duration int `json:"duration"`
			Interval int `json:"interval"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.PID == 0 {
			return nil, fmt.Errorf("process.sample requires {\"pid\": <pid>}")
		}
		if p.Duration == 0 {
			p.Duration = 1
		}
		if p.Interval == 0 {
			p.Interval = 10
		}
		out, err := runSampleCmd(p.PID, p.Duration, p.Interval)
		if err != nil {
			return nil, fmt.Errorf("sample pid %d: %w", p.PID, err)
		}
		return map[string]any{"pid": p.PID, "output": string(out)}, nil
	})

	// power.state — battery level, thermal pressure, assertions, and top energy users.
	d.Register("power.state", func(_ json.RawMessage) (any, error) {
		return sysinfo.CollectPower(sysinfo.DefaultRunner), nil
	})

	// storage.system — disk volumes + ~/Library usage summary.
	d.Register("storage.system", func(_ json.RawMessage) (any, error) {
		return storagestate.Collect(storagestate.CollectOptions{}), nil
	})

	// storage.byApp — per-app ~/Library usage.
	// { "paths": ["/Applications/Foo.app", ...] }
	d.Register("storage.byApp", func(params json.RawMessage) (any, error) {
		var p struct {
			Paths []string `json:"paths"`
		}
		if err := json.Unmarshal(params, &p); err != nil || len(p.Paths) == 0 {
			return nil, fmt.Errorf("storage.byApp requires {\"paths\": [...]}")
		}
		return storagestate.Collect(storagestate.CollectOptions{
			AppPaths: p.Paths,
		}), nil
	})

	// cache.stats — per-kind entry count, bytes on disk, and last-write time.
	d.Register("cache.stats", func(_ json.RawMessage) (any, error) {
		stats, err := cacheReg.Stats()
		if err != nil {
			return nil, fmt.Errorf("cache stats: %w", err)
		}
		return stats, nil
	})

	// cache.clear — evict cached data.
	// Optional: { "kind": "detect" } to clear a single kind; omit for all.
	d.Register("cache.clear", func(params json.RawMessage) (any, error) {
		var p struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(params, &p)
		if err := cacheReg.Clear(p.Kind); err != nil {
			return nil, fmt.Errorf("cache clear: %w", err)
		}
		if p.Kind == "" {
			return map[string]any{"cleared": "all"}, nil
		}
		return map[string]any{"cleared": p.Kind}, nil
	})

	// helper.* — proxy to the privileged helper when available.
	hc := helperclient.New()

	d.Register("helper.health", func(_ json.RawMessage) (any, error) {
		result, err := hc.Health()
		if err != nil {
			if helperclient.IsUnavailable(err) {
				return map[string]any{"ok": false, "helper": false, "reason": "helper not running"}, nil
			}
			return nil, err
		}
		return result, nil
	})

	d.Register("helper.powermetrics.sample", func(params json.RawMessage) (any, error) {
		var p struct {
			DurationMS int `json:"duration_ms"`
		}
		_ = json.Unmarshal(params, &p)
		result, err := hc.PowermetricsSample(p.DurationMS)
		if err != nil {
			if helperclient.IsUnavailable(err) {
				return nil, fmt.Errorf("privileged helper not running; install with: sudo spectra install-helper")
			}
			return nil, err
		}
		return result, nil
	})

	d.Register("helper.tcc.system.query", func(params json.RawMessage) (any, error) {
		var p struct {
			BundleID string `json:"bundle_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.BundleID == "" {
			return nil, fmt.Errorf("helper.tcc.system.query requires {\"bundle_id\":\"...\"}")
		}
		result, err := hc.TCCSystemQuery(p.BundleID)
		if err != nil {
			if helperclient.IsUnavailable(err) {
				return nil, fmt.Errorf("privileged helper not running; install with: sudo spectra install-helper")
			}
			return nil, err
		}
		return result, nil
	})
}

func daemonJVMOptions() jvm.CollectOptions {
	return jvm.CollectOptions{JDKs: collectJDKs(context.Background(), toolchain.CollectOptions{})}
}

// runSampleCmd runs `sample <pid> <duration> <interval>` and returns stdout.
func runSampleCmd(pid, durationSec, intervalMS int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(durationSec+5)*time.Second)
	defer cancel()
	// #nosec G204 -- PID, duration, and interval are integer arguments.
	return exec.CommandContext(ctx, "sample",
		strconv.Itoa(pid),
		strconv.Itoa(durationSec),
		strconv.Itoa(intervalMS),
	).Output()
}
