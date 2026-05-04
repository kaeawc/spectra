// Package store persists Spectra snapshots to a local SQLite database.
// It uses modernc.org/sqlite (pure Go, no CGo) so the binary cross-compiles
// trivially. The database lives at ~/.spectra/spectra.db by default.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

// DefaultPath returns ~/.spectra/spectra.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".spectra", "spectra.db"), nil
}

// DB is a thin wrapper around *sql.DB that owns schema setup and the
// canonical read/write operations for Spectra's relational tier.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path. It applies all
// pragmas and runs the schema migration on first use. Caller is
// responsible for Close().
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One writer at a time is fine; WAL allows concurrent readers.
	db.SetMaxOpenConns(1)

	s := &DB{db: db}
	if err := s.applyPragmas(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database connection.
func (s *DB) Close() error { return s.db.Close() }

func (s *DB) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("store: pragma %q: %w", p, err)
		}
	}
	return nil
}

// migrate creates tables that don't yet exist. Idempotent.
// Also adds new columns to existing tables when the schema evolves.
func (s *DB) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Add snapshot_json column if this is an existing DB without it.
	_, _ = s.db.Exec(`ALTER TABLE snapshots ADD COLUMN snapshot_json TEXT`)
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS hosts (
    machine_uuid  TEXT PRIMARY KEY,
    hostname      TEXT NOT NULL,
    os_name       TEXT NOT NULL,
    os_version    TEXT NOT NULL,
    os_build      TEXT,
    cpu_brand     TEXT,
    cpu_cores     INTEGER,
    ram_bytes     INTEGER,
    architecture  TEXT,
    first_seen    DATETIME NOT NULL,
    last_seen     DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS snapshots (
    id            TEXT PRIMARY KEY,
    machine_uuid  TEXT NOT NULL,
    taken_at      DATETIME NOT NULL,
    kind          TEXT NOT NULL DEFAULT 'live',
    spectra_ver   TEXT,
    app_count     INTEGER NOT NULL DEFAULT 0,
    snapshot_json TEXT,
    FOREIGN KEY (machine_uuid) REFERENCES hosts(machine_uuid)
);

CREATE INDEX IF NOT EXISTS idx_snapshots_machine ON snapshots(machine_uuid, taken_at DESC);

CREATE TABLE IF NOT EXISTS snapshot_apps (
    snapshot_id   TEXT NOT NULL,
    bundle_id     TEXT,
    app_name      TEXT NOT NULL,
    app_path      TEXT NOT NULL,
    ui            TEXT,
    runtime       TEXT,
    packaging     TEXT,
    confidence    TEXT,
    app_version   TEXT,
    architectures TEXT,    -- JSON array
    result_json   TEXT NOT NULL, -- full detect.Result marshalled
    PRIMARY KEY (snapshot_id, app_path),
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
);

CREATE INDEX IF NOT EXISTS idx_snapshot_apps_bundle ON snapshot_apps(bundle_id);
`

// SnapshotRow is a lightweight summary row from the snapshots table.
type SnapshotRow struct {
	ID          string
	MachineUUID string
	TakenAt     time.Time
	Kind        string
	SpectraVer  string
	AppCount    int
}

// SaveSnapshot persists snap and all its apps. It upserts the host row
// and inserts the snapshot + apps rows inside a single transaction.
func (s *DB) SaveSnapshot(ctx context.Context, snap SnapshotInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := upsertHost(tx, snap); err != nil {
		return err
	}
	if err := insertSnapshot(tx, snap); err != nil {
		return err
	}
	for _, app := range snap.Apps {
		if err := insertApp(tx, snap.ID, app); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func upsertHost(tx *sql.Tx, s SnapshotInput) error {
	_, err := tx.Exec(`
		INSERT INTO hosts
		    (machine_uuid, hostname, os_name, os_version, os_build,
		     cpu_brand, cpu_cores, ram_bytes, architecture, first_seen, last_seen)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(machine_uuid) DO UPDATE SET
		    hostname     = excluded.hostname,
		    os_version   = excluded.os_version,
		    os_build     = excluded.os_build,
		    cpu_brand    = excluded.cpu_brand,
		    cpu_cores    = excluded.cpu_cores,
		    ram_bytes    = excluded.ram_bytes,
		    architecture = excluded.architecture,
		    last_seen    = excluded.last_seen`,
		s.MachineUUID, s.Hostname, s.OSName, s.OSVersion, s.OSBuild,
		s.CPUBrand, s.CPUCores, s.RAMBytes, s.Architecture,
		s.TakenAt, s.TakenAt,
	)
	return err
}

func insertSnapshot(tx *sql.Tx, s SnapshotInput) error {
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO snapshots (id, machine_uuid, taken_at, kind, spectra_ver, app_count, snapshot_json)
		VALUES (?,?,?,?,?,?,?)`,
		s.ID, s.MachineUUID, s.TakenAt, s.Kind, s.SpectraVer, len(s.Apps), nullableJSON(s.SnapshotJSON),
	)
	return err
}

func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func insertApp(tx *sql.Tx, snapID string, a AppInput) error {
	archJSON, _ := json.Marshal(a.Architectures)
	resultJSON, _ := json.Marshal(a.ResultJSON)
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO snapshot_apps
		    (snapshot_id, bundle_id, app_name, app_path, ui, runtime,
		     packaging, confidence, app_version, architectures, result_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		snapID, a.BundleID, a.AppName, a.AppPath,
		a.UI, a.Runtime, a.Packaging, a.Confidence,
		a.AppVersion, string(archJSON), string(resultJSON),
	)
	return err
}

// ListSnapshots returns summary rows ordered newest-first.
// Pass machine_uuid="" to list all hosts.
func (s *DB) ListSnapshots(ctx context.Context, machineUUID string) ([]SnapshotRow, error) {
	q := `SELECT id, machine_uuid, taken_at, kind, COALESCE(spectra_ver,''), app_count
	      FROM snapshots ORDER BY taken_at DESC LIMIT 100`
	args := []any{}
	if machineUUID != "" {
		q = `SELECT id, machine_uuid, taken_at, kind, COALESCE(spectra_ver,''), app_count
		     FROM snapshots WHERE machine_uuid=? ORDER BY taken_at DESC LIMIT 100`
		args = append(args, machineUUID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnapshotRow
	for rows.Next() {
		var r SnapshotRow
		var takenAt string
		if err := rows.Scan(&r.ID, &r.MachineUUID, &takenAt, &r.Kind, &r.SpectraVer, &r.AppCount); err != nil {
			return nil, err
		}
		r.TakenAt, _ = time.Parse(time.RFC3339, takenAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSnapshot returns the named snapshot row, or ErrNotFound if absent.
func (s *DB) GetSnapshot(ctx context.Context, id string) (SnapshotRow, error) {
	var r SnapshotRow
	var takenAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, machine_uuid, taken_at, kind, COALESCE(spectra_ver,''), app_count
		 FROM snapshots WHERE id=?`, id,
	).Scan(&r.ID, &r.MachineUUID, &takenAt, &r.Kind, &r.SpectraVer, &r.AppCount)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.TakenAt, _ = time.Parse(time.RFC3339, takenAt)
	return r, nil
}

// GetSnapshotJSON returns the full snapshot JSON blob for id, or ErrNotFound
// if absent. Returns nil (not ErrNotFound) if the row exists but was saved
// without a JSON blob (older rows).
func (s *DB) GetSnapshotJSON(ctx context.Context, id string) ([]byte, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT snapshot_json FROM snapshots WHERE id=?`, id,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, nil
	}
	return []byte(raw.String), nil
}

// GetSnapshotApps returns the per-app rows for a snapshot.
func (s *DB) GetSnapshotApps(ctx context.Context, snapID string) ([]AppRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT app_name, app_path, ui, runtime, packaging, confidence, app_version
		 FROM snapshot_apps WHERE snapshot_id=? ORDER BY app_name`, snapID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppRow
	for rows.Next() {
		var a AppRow
		if err := rows.Scan(&a.AppName, &a.AppPath, &a.UI, &a.Runtime,
			&a.Packaging, &a.Confidence, &a.AppVersion); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AppRow is a lightweight per-app row from snapshot_apps.
type AppRow struct {
	AppName    string
	AppPath    string
	UI         string
	Runtime    string
	Packaging  string
	Confidence string
	AppVersion string
}

// ErrNotFound is returned when a requested record doesn't exist.
var ErrNotFound = errors.New("store: not found")

// SnapshotInput carries the data needed to persist a snapshot.
type SnapshotInput struct {
	ID           string
	MachineUUID  string
	TakenAt      time.Time
	Kind         string
	SpectraVer   string
	Hostname     string
	OSName       string
	OSVersion    string
	OSBuild      string
	CPUBrand     string
	CPUCores     int
	RAMBytes     uint64
	Architecture string
	Apps         []AppInput
	// SnapshotJSON is the full snapshot marshalled to JSON. Stored as a blob
	// so diff and rules can reconstruct the complete Snapshot without re-running collectors.
	SnapshotJSON []byte
}

// AppInput carries the per-app data to store.
type AppInput struct {
	BundleID      string
	AppName       string
	AppPath       string
	UI            string
	Runtime       string
	Packaging     string
	Confidence    string
	AppVersion    string
	Architectures []string
	ResultJSON    any // the full detect.Result, marshalled to JSON
}
