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

	"github.com/kaeawc/spectra/internal/detect"
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

	d := rpc.NewDispatcher()
	registerHandlers(d, opts.SpectraVersion, db)

	// Close the listener when ctx is cancelled to unblock Accept.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	return d.ServeListener(ln)
}

// registerHandlers wires all JSON-RPC methods into d.
func registerHandlers(d *rpc.Dispatcher, version string, db *store.DB) {
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
