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
	"path/filepath"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/diff"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
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
	SpectraVersion string
	DBPath         string // empty = store.DefaultPath()
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
	defer func() {
		ln.Close()
		os.Remove(sockPath)
	}()

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

	d := rpc.NewDispatcher()
	registerHandlers(d, opts.SpectraVersion, db, collector)

	// Close the listener when ctx is cancelled to unblock Accept.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	return d.ServeListener(ln)
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

// registerHandlers wires all JSON-RPC methods into d.
func registerHandlers(d *rpc.Dispatcher, version string, db *store.DB, collector *metrics.Collector) {
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
		})
		if err := db.SaveSnapshot(context.Background(), store.FromSnapshot(snap)); err != nil {
			return nil, err
		}
		return snap, nil
	})

	d.Register("process.live", func(params json.RawMessage) (any, error) {
		// Returns the most recent samples for all known PIDs.
		var p struct{ Limit int `json:"limit"` }
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

	d.Register("rules.check", func(params json.RawMessage) (any, error) {
		// Optional: { "snapshot_id": "snap-..." } to evaluate against stored snapshot.
		var p struct{ SnapshotID string `json:"snapshot_id"` }
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
			})
		}
		return rules.Evaluate(snap, rules.V1Catalog()), nil
	})
}
