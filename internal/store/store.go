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

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/snapshot"

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
	db    *sql.DB
	clock clock.Clock
	ids   idgen.Generator
}

// Options configures DB dependencies that need deterministic tests.
type Options struct {
	Clock       clock.Clock
	IDGenerator idgen.Generator
}

// Open opens (or creates) the SQLite database at path. It applies all
// pragmas and runs the schema migration on first use. Caller is
// responsible for Close().
func Open(path string) (*DB, error) {
	return OpenWithOptions(path, Options{})
}

// OpenWithOptions opens a database with injectable time and ID generation.
func OpenWithOptions(path string, opts Options) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One writer at a time is fine; WAL allows concurrent readers.
	db.SetMaxOpenConns(1)

	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.IDGenerator == nil {
		opts.IDGenerator = idgen.UUID{}
	}

	s := &DB{db: db, clock: opts.Clock, ids: opts.IDGenerator}
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
	// jvm_samples shipped briefly with second-resolution at_unix. Drop the
	// old shape so the new nanosecond schema applies — no real users yet.
	if hasJVMSamplesAtUnix(s.db) {
		if _, err := s.db.Exec(`DROP TABLE IF EXISTS jvm_samples`); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// Add snapshot_json column if this is an existing DB without it.
	_, _ = s.db.Exec(`ALTER TABLE snapshots ADD COLUMN snapshot_json TEXT`)
	return nil
}

// hasJVMSamplesAtUnix reports whether jvm_samples exists with the legacy
// at_unix column. Used only by migrate() to decide whether to recreate it.
func hasJVMSamplesAtUnix(db *sql.DB) bool {
	rows, err := db.Query(`PRAGMA table_info(jvm_samples)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == "at_unix" {
			return true
		}
	}
	return false
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
    name          TEXT,
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

CREATE TABLE IF NOT EXISTS snapshot_processes (
    snapshot_id TEXT NOT NULL,
    pid         INTEGER NOT NULL,
    ppid        INTEGER NOT NULL DEFAULT 0,
    command     TEXT NOT NULL DEFAULT '',
    rss_kib     INTEGER NOT NULL DEFAULT 0,
    cpu_pct     REAL NOT NULL DEFAULT 0,
    app_path    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (snapshot_id, pid),
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
);

CREATE INDEX IF NOT EXISTS idx_snapshot_processes_snap ON snapshot_processes(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_snapshot_processes_app  ON snapshot_processes(app_path) WHERE app_path != '';

CREATE TABLE IF NOT EXISTS login_items (
    snapshot_id  TEXT NOT NULL,
    bundle_id    TEXT NOT NULL DEFAULT '',
    plist_path   TEXT NOT NULL,
    label        TEXT NOT NULL DEFAULT '',
    scope        TEXT NOT NULL DEFAULT '',
    daemon       INTEGER NOT NULL DEFAULT 0,
    run_at_load  INTEGER NOT NULL DEFAULT 0,
    keep_alive   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (snapshot_id, plist_path),
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
);

CREATE INDEX IF NOT EXISTS idx_login_items_snap   ON login_items(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_login_items_bundle ON login_items(bundle_id) WHERE bundle_id != '';

CREATE TABLE IF NOT EXISTS granted_perms (
    snapshot_id  TEXT NOT NULL,
    bundle_id    TEXT NOT NULL,
    service      TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, bundle_id, service),
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
);

CREATE INDEX IF NOT EXISTS idx_granted_perms_snap   ON granted_perms(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_granted_perms_bundle ON granted_perms(bundle_id);

CREATE TABLE IF NOT EXISTS process_metrics (
    pid          INTEGER NOT NULL,
    minute_at    DATETIME NOT NULL,
    avg_rss_kib  INTEGER NOT NULL DEFAULT 0,
    max_rss_kib  INTEGER NOT NULL DEFAULT 0,
    avg_cpu_pct  REAL NOT NULL DEFAULT 0,
    max_cpu_pct  REAL NOT NULL DEFAULT 0,
    sample_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (pid, minute_at)
);

CREATE INDEX IF NOT EXISTS idx_process_metrics_pid ON process_metrics(pid, minute_at DESC);

CREATE TABLE IF NOT EXISTS jvm_samples (
    pid          INTEGER NOT NULL,
    at_nano      INTEGER NOT NULL,
    old_gen_pct  REAL NOT NULL DEFAULT 0,
    fgc          INTEGER NOT NULL DEFAULT 0,
    fgct         REAL NOT NULL DEFAULT 0,
    heap_mb      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (pid, at_nano)
);

CREATE INDEX IF NOT EXISTS idx_jvm_samples_pid ON jvm_samples(pid, at_nano DESC);

CREATE TABLE IF NOT EXISTS issues (
    id                     TEXT PRIMARY KEY,
    rule_id                TEXT NOT NULL,
    machine_uuid           TEXT NOT NULL,
    subject                TEXT NOT NULL DEFAULT '',
    severity               TEXT NOT NULL,
    message                TEXT NOT NULL,
    fix                    TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL DEFAULT 'open',
    first_seen_snapshot_id TEXT,
    last_seen_snapshot_id  TEXT,
    created_at             DATETIME NOT NULL,
    updated_at             DATETIME NOT NULL,
    FOREIGN KEY (machine_uuid) REFERENCES hosts(machine_uuid)
);

CREATE INDEX IF NOT EXISTS idx_issues_machine ON issues(machine_uuid, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_issues_rule ON issues(rule_id, status);

CREATE TABLE IF NOT EXISTS applied_fixes (
    id         TEXT PRIMARY KEY,
    issue_id   TEXT NOT NULL,
    applied_at DATETIME NOT NULL,
    applied_by TEXT NOT NULL DEFAULT '',
    command    TEXT NOT NULL DEFAULT '',
    output     TEXT NOT NULL DEFAULT '',
    exit_code  INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (issue_id) REFERENCES issues(id)
);

CREATE INDEX IF NOT EXISTS idx_applied_fixes_issue ON applied_fixes(issue_id, applied_at DESC);
`

// SnapshotRow is a lightweight summary row from the snapshots table.
type SnapshotRow struct {
	ID          string
	MachineUUID string
	TakenAt     time.Time
	Kind        string
	Name        string // optional human label, used for baselines
	SpectraVer  string
	AppCount    int
}

// HostRow is a summary row from the hosts table.
type HostRow struct {
	MachineUUID   string    `json:"machine_uuid"`
	Hostname      string    `json:"hostname"`
	OSName        string    `json:"os_name"`
	OSVersion     string    `json:"os_version"`
	OSBuild       string    `json:"os_build,omitempty"`
	CPUBrand      string    `json:"cpu_brand,omitempty"`
	CPUCores      int       `json:"cpu_cores,omitempty"`
	RAMBytes      int64     `json:"ram_bytes,omitempty"`
	Architecture  string    `json:"architecture,omitempty"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	SnapshotCount int       `json:"snapshot_count"`
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
	name := sql.NullString{String: s.Name, Valid: s.Name != ""}
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO snapshots (id, machine_uuid, taken_at, kind, name, spectra_ver, app_count, snapshot_json)
		VALUES (?,?,?,?,?,?,?,?)`,
		s.ID, s.MachineUUID, s.TakenAt, s.Kind, name, s.SpectraVer, len(s.Apps), nullableJSON(s.SnapshotJSON),
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

// ListHosts returns known hosts ordered by most recent snapshot.
func (s *DB) ListHosts(ctx context.Context) ([]HostRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.machine_uuid, h.hostname, h.os_name, h.os_version,
		       COALESCE(h.os_build, ''), COALESCE(h.cpu_brand, ''),
		       COALESCE(h.cpu_cores, 0), COALESCE(h.ram_bytes, 0),
		       COALESCE(h.architecture, ''), h.first_seen, h.last_seen,
		       COUNT(sn.id)
		FROM hosts h
		LEFT JOIN snapshots sn ON sn.machine_uuid = h.machine_uuid
		GROUP BY h.machine_uuid, h.hostname, h.os_name, h.os_version,
		         h.os_build, h.cpu_brand, h.cpu_cores, h.ram_bytes,
		         h.architecture, h.first_seen, h.last_seen
		ORDER BY h.last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HostRow
	for rows.Next() {
		var r HostRow
		var firstSeen string
		var lastSeen string
		if err := rows.Scan(
			&r.MachineUUID,
			&r.Hostname,
			&r.OSName,
			&r.OSVersion,
			&r.OSBuild,
			&r.CPUBrand,
			&r.CPUCores,
			&r.RAMBytes,
			&r.Architecture,
			&firstSeen,
			&lastSeen,
			&r.SnapshotCount,
		); err != nil {
			return nil, err
		}
		r.FirstSeen, _ = time.Parse(time.RFC3339, firstSeen)
		r.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSnapshots returns summary rows ordered newest-first.
// Pass machine_uuid="" to list all hosts. Pass kind="" to list all kinds.
func (s *DB) ListSnapshots(ctx context.Context, machineUUID string) ([]SnapshotRow, error) {
	q := `SELECT id, machine_uuid, taken_at, kind, COALESCE(name,''), COALESCE(spectra_ver,''), app_count
	      FROM snapshots ORDER BY taken_at DESC LIMIT 100`
	args := []any{}
	if machineUUID != "" {
		q = `SELECT id, machine_uuid, taken_at, kind, COALESCE(name,''), COALESCE(spectra_ver,''), app_count
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
		if err := rows.Scan(&r.ID, &r.MachineUUID, &takenAt, &r.Kind, &r.Name, &r.SpectraVer, &r.AppCount); err != nil {
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
		`SELECT id, machine_uuid, taken_at, kind, COALESCE(name,''), COALESCE(spectra_ver,''), app_count
		 FROM snapshots WHERE id=?`, id,
	).Scan(&r.ID, &r.MachineUUID, &takenAt, &r.Kind, &r.Name, &r.SpectraVer, &r.AppCount)
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

// DeleteSnapshot deletes a snapshot and all its child rows by ID.
func (s *DB) DeleteSnapshot(ctx context.Context, id string) error {
	for _, table := range []string{"snapshot_processes", "login_items", "granted_perms", "snapshot_apps"} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE snapshot_id=?`, id); err != nil { // #nosec G202 — table is a hardcoded literal, not user input
			return fmt.Errorf("store: delete %s for %s: %w", table, id, err)
		}
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("store: delete snapshot %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
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

// ProcessSnapshotRow is one row from snapshot_processes.
type ProcessSnapshotRow struct {
	PID     int
	PPID    int
	Command string
	RSSKiB  int64
	CPUPct  float64
	AppPath string
}

// SaveSnapshotProcesses inserts process rows for snapID. Uses INSERT OR IGNORE
// to be idempotent (safe to call multiple times for the same snapshot).
func (s *DB) SaveSnapshotProcesses(ctx context.Context, snapID string, procs []ProcessSnapshotRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, p := range procs {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO snapshot_processes
			  (snapshot_id, pid, ppid, command, rss_kib, cpu_pct, app_path)
			VALUES (?,?,?,?,?,?,?)`,
			snapID, p.PID, p.PPID, p.Command, p.RSSKiB, p.CPUPct, p.AppPath,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSnapshotProcesses returns all process rows for snapID.
func (s *DB) GetSnapshotProcesses(ctx context.Context, snapID string) ([]ProcessSnapshotRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pid, ppid, command, rss_kib, cpu_pct, app_path
		 FROM snapshot_processes WHERE snapshot_id=? ORDER BY rss_kib DESC`, snapID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessSnapshotRow
	for rows.Next() {
		var r ProcessSnapshotRow
		if err := rows.Scan(&r.PID, &r.PPID, &r.Command, &r.RSSKiB, &r.CPUPct, &r.AppPath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoginItemRow is one row from login_items.
type LoginItemRow struct {
	SnapshotID string
	BundleID   string
	PlistPath  string
	Label      string
	Scope      string
	Daemon     bool
	RunAtLoad  bool
	KeepAlive  bool
}

// SaveLoginItems inserts login item rows for snapID. Uses INSERT OR IGNORE.
func (s *DB) SaveLoginItems(ctx context.Context, snapID string, items []LoginItemRow) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, it := range items {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO login_items
			  (snapshot_id, bundle_id, plist_path, label, scope, daemon, run_at_load, keep_alive)
			VALUES (?,?,?,?,?,?,?,?)`,
			snapID, it.BundleID, it.PlistPath, it.Label, it.Scope,
			boolInt(it.Daemon), boolInt(it.RunAtLoad), boolInt(it.KeepAlive),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetLoginItems returns all login item rows for snapID.
func (s *DB) GetLoginItems(ctx context.Context, snapID string) ([]LoginItemRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT bundle_id, plist_path, label, scope, daemon, run_at_load, keep_alive
		 FROM login_items WHERE snapshot_id=? ORDER BY plist_path`, snapID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoginItemRow
	for rows.Next() {
		var r LoginItemRow
		var daemon, runAtLoad, keepAlive int
		if err := rows.Scan(&r.BundleID, &r.PlistPath, &r.Label, &r.Scope,
			&daemon, &runAtLoad, &keepAlive); err != nil {
			return nil, err
		}
		r.SnapshotID = snapID
		r.Daemon = daemon != 0
		r.RunAtLoad = runAtLoad != 0
		r.KeepAlive = keepAlive != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// GrantedPermRow is one row from granted_perms.
type GrantedPermRow struct {
	SnapshotID string
	BundleID   string
	Service    string
}

// SaveGrantedPerms inserts TCC permission rows for snapID. Uses INSERT OR IGNORE.
func (s *DB) SaveGrantedPerms(ctx context.Context, snapID string, perms []GrantedPermRow) error {
	if len(perms) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, p := range perms {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO granted_perms (snapshot_id, bundle_id, service)
			VALUES (?,?,?)`,
			snapID, p.BundleID, p.Service,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetGrantedPerms returns all TCC permission rows for snapID.
func (s *DB) GetGrantedPerms(ctx context.Context, snapID string) ([]GrantedPermRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT bundle_id, service FROM granted_perms
		 WHERE snapshot_id=? ORDER BY bundle_id, service`, snapID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GrantedPermRow
	for rows.Next() {
		var r GrantedPermRow
		if err := rows.Scan(&r.BundleID, &r.Service); err != nil {
			return nil, err
		}
		r.SnapshotID = snapID
		out = append(out, r)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SaveProcessMetrics persists a batch of 1-minute aggregates from the ring
// buffer. Rows are upserted because each flush may include a more complete
// aggregate for a minute that was already partially written.
func (s *DB) SaveProcessMetrics(ctx context.Context, aggs []ProcessMetricRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, a := range aggs {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO process_metrics
			    (pid, minute_at, avg_rss_kib, max_rss_kib, avg_cpu_pct, max_cpu_pct, sample_count)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(pid, minute_at) DO UPDATE SET
			    avg_rss_kib=excluded.avg_rss_kib,
			    max_rss_kib=excluded.max_rss_kib,
			    avg_cpu_pct=excluded.avg_cpu_pct,
			    max_cpu_pct=excluded.max_cpu_pct,
			    sample_count=excluded.sample_count`,
			a.PID, a.MinuteAt.UTC().Format(time.RFC3339),
			a.AvgRSSKiB, a.MaxRSSKiB,
			a.AvgCPUPct, a.MaxCPUPct,
			a.SampleCount,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetProcessMetrics returns 1-minute aggregate rows for pid, newest first,
// up to limit rows. Pass limit=0 for no cap (up to 1000).
func (s *DB) GetProcessMetrics(ctx context.Context, pid, limit int) ([]ProcessMetricRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pid, minute_at, avg_rss_kib, max_rss_kib, avg_cpu_pct, max_cpu_pct, sample_count
		FROM process_metrics WHERE pid=?
		ORDER BY minute_at DESC LIMIT ?`, pid, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessMetricRow
	for rows.Next() {
		var r ProcessMetricRow
		var minuteAt string
		if err := rows.Scan(&r.PID, &minuteAt, &r.AvgRSSKiB, &r.MaxRSSKiB,
			&r.AvgCPUPct, &r.MaxCPUPct, &r.SampleCount); err != nil {
			return nil, err
		}
		r.MinuteAt, _ = time.Parse(time.RFC3339, minuteAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAllProcessMetrics returns the most recent rows across all PIDs, ordered
// by minute_at DESC. limit caps total rows returned (0 → 1000).
func (s *DB) GetAllProcessMetrics(ctx context.Context, limit int) ([]ProcessMetricRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pid, minute_at, avg_rss_kib, max_rss_kib, avg_cpu_pct, max_cpu_pct, sample_count
		FROM process_metrics
		ORDER BY minute_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessMetricRow
	for rows.Next() {
		var r ProcessMetricRow
		var minuteAt string
		if err := rows.Scan(&r.PID, &minuteAt, &r.AvgRSSKiB, &r.MaxRSSKiB,
			&r.AvgCPUPct, &r.MaxCPUPct, &r.SampleCount); err != nil {
			return nil, err
		}
		r.MinuteAt, _ = time.Parse(time.RFC3339, minuteAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ProcessMetricRow is one 1-minute aggregate row from process_metrics.
type ProcessMetricRow struct {
	PID         int
	MinuteAt    time.Time
	AvgRSSKiB   int64
	MaxRSSKiB   int64
	AvgCPUPct   float64
	MaxCPUPct   float64
	SampleCount int
}

// SaveJVMSamples upserts compact per-PID JVM samples used by trend-aware
// rules. Timestamps are stored as nanoseconds so two parallel diagnose
// calls within the same wall-clock second produce distinct rows; the
// upsert clause keeps reruns at literally the same instant idempotent.
func (s *DB) SaveJVMSamples(ctx context.Context, samples []snapshot.JVMSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, sm := range samples {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO jvm_samples (pid, at_nano, old_gen_pct, fgc, fgct, heap_mb)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(pid, at_nano) DO UPDATE SET
			    old_gen_pct=excluded.old_gen_pct,
			    fgc=excluded.fgc,
			    fgct=excluded.fgct,
			    heap_mb=excluded.heap_mb`,
			sm.PID, sm.At.UTC().UnixNano(), sm.OldGenPct, sm.FGC, sm.FGCT, sm.HeapMB,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetRecentJVMSamples returns up to limit JVM samples for pid, ordered
// oldest-first so callers can hand the slice straight to trend predicates.
// Pass limit=0 for the default cap (60 samples).
func (s *DB) GetRecentJVMSamples(ctx context.Context, pid, limit int) ([]snapshot.JVMSample, error) {
	if limit <= 0 {
		limit = 60
	}
	// Pull newest-first so LIMIT applies to the most recent rows, then reverse.
	rows, err := s.db.QueryContext(ctx, `
		SELECT pid, at_nano, old_gen_pct, fgc, fgct, heap_mb
		FROM jvm_samples WHERE pid=?
		ORDER BY at_nano DESC LIMIT ?`, pid, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []snapshot.JVMSample
	for rows.Next() {
		var sm snapshot.JVMSample
		var atNano int64
		if err := rows.Scan(&sm.PID, &atNano, &sm.OldGenPct, &sm.FGC, &sm.FGCT, &sm.HeapMB); err != nil {
			return nil, err
		}
		sm.At = time.Unix(0, atNano).UTC()
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to oldest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// AttachJVMHistory persists the current snapshot's JVMs as samples and
// loads recent samples per PID into snap.JVMHistory so trend-aware rules
// see a multi-sample window. Errors are accumulated but never abort the
// caller — history is an enhancement, not a contract; rules degrade to
// point-in-time checks when nothing is loaded.
//
// The snapshot's TakenAt is used as the sample timestamp when set, so
// historical snapshots replayed against the store don't get a time.Now()
// stamp that would corrupt trend ordering.
func (s *DB) AttachJVMHistory(ctx context.Context, snap *snapshot.Snapshot) {
	if snap == nil || len(snap.JVMs) == 0 {
		return
	}
	now := snap.TakenAt
	if now.IsZero() {
		now = time.Now()
	}
	current := make([]snapshot.JVMSample, 0, len(snap.JVMs))
	for _, j := range snap.JVMs {
		if sm, ok := snapshot.JVMSampleFrom(j, now); ok {
			current = append(current, sm)
		}
	}
	_ = s.SaveJVMSamples(ctx, current)

	var history snapshot.JVMHistory
	for _, j := range snap.JVMs {
		samples, err := s.GetRecentJVMSamples(ctx, j.PID, 0)
		if err != nil {
			continue
		}
		history = append(history, samples...)
	}
	snap.JVMHistory = history
}

// PruneJVMSamples deletes jvm_samples rows older than keepDays. Returns the
// number of rows removed. keepDays <= 0 keeps the default of 7 days.
func (s *DB) PruneJVMSamples(ctx context.Context, keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = 7
	}
	cutoff := time.Now().UTC().Add(-time.Duration(keepDays) * 24 * time.Hour).UnixNano()
	res, err := s.db.ExecContext(ctx, `DELETE FROM jvm_samples WHERE at_nano < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneSnapshots deletes live snapshots beyond the retention limit for each
// machine. Baselines (kind != "live") are never deleted. The default limit
// is 100 live snapshots per machine UUID.
func (s *DB) PruneSnapshots(ctx context.Context, keepN int) (int64, error) {
	if keepN <= 0 {
		keepN = 100
	}
	// Find all machine UUIDs that have live snapshots.
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT machine_uuid FROM snapshots WHERE kind='live'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var machines []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return 0, err
		}
		machines = append(machines, m)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	var total int64
	for _, m := range machines {
		// Delete child rows for to-be-pruned snapshots first (FK constraints).
		pruneSubquery := `SELECT id FROM snapshots WHERE kind='live' AND machine_uuid=?
				AND id NOT IN (SELECT id FROM snapshots WHERE kind='live' AND machine_uuid=? ORDER BY taken_at DESC LIMIT ?)`
		for _, table := range []string{"snapshot_processes", "login_items", "granted_perms", "snapshot_apps"} {
			// #nosec G202 -- table is selected from hardcoded literals, not user input.
			if _, err := s.db.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE snapshot_id IN (`+pruneSubquery+`)`,
				m, m, keepN); err != nil {
				return total, fmt.Errorf("store: prune %s %s: %w", table, m, err)
			}
		}
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM snapshots
			WHERE kind='live' AND machine_uuid=?
			  AND id NOT IN (
			    SELECT id FROM snapshots
			    WHERE kind='live' AND machine_uuid=?
			    ORDER BY taken_at DESC
			    LIMIT ?
			  )`, m, m, keepN)
		if err != nil {
			return total, fmt.Errorf("store: prune %s: %w", m, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// ErrNotFound is returned when a requested record doesn't exist.
var ErrNotFound = errors.New("store: not found")

// SnapshotInput carries the data needed to persist a snapshot.
type SnapshotInput struct {
	ID           string
	MachineUUID  string
	TakenAt      time.Time
	Kind         string
	Name         string // optional label for baselines
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

// IssueStatus enumerates the lifecycle states for an issue.
type IssueStatus string

const (
	IssueOpen         IssueStatus = "open"
	IssueAcknowledged IssueStatus = "acknowledged"
	IssueDismissed    IssueStatus = "dismissed"
	IssueFixed        IssueStatus = "fixed"
	IssueClosed       IssueStatus = "closed"
)

// IssueRow is one row from the issues table.
type IssueRow struct {
	ID                  string
	RuleID              string
	MachineUUID         string
	Subject             string
	Severity            string
	Message             string
	Fix                 string
	Status              IssueStatus
	FirstSeenSnapshotID string
	LastSeenSnapshotID  string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// AppliedFixRow is one row from the applied_fixes table.
type AppliedFixRow struct {
	ID        string
	IssueID   string
	AppliedAt time.Time
	AppliedBy string
	Command   string
	Output    string
	ExitCode  int
}

// UpsertIssues reconciles a slice of findings against the issues table.
// For each finding, it looks up an existing open/acknowledged issue by
// (rule_id, machine_uuid, subject). If found, last_seen_snapshot_id and
// updated_at are refreshed. Dismissed issues suppress later matching
// findings. If no active or dismissed issue exists, a new open issue is
// inserted.
// Returns the IDs of all touched (inserted or updated) issues.
func (s *DB) UpsertIssues(ctx context.Context, machineUUID, snapshotID string, findings []FindingInput) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	now := s.nowString()
	var ids []string
	for _, f := range findings {
		var existingID string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM issues
			WHERE rule_id=? AND machine_uuid=? AND subject=?
			  AND status IN ('open','acknowledged')
			LIMIT 1`,
			f.RuleID, machineUUID, f.Subject,
		).Scan(&existingID)

		if err == nil {
			// Existing active issue — refresh last_seen.
			_, err = tx.ExecContext(ctx, `
				UPDATE issues SET last_seen_snapshot_id=?, updated_at=? WHERE id=?`,
				snapshotID, now, existingID)
			if err != nil {
				return nil, fmt.Errorf("store: update issue: %w", err)
			}
			ids = append(ids, existingID)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: lookup issue: %w", err)
		}
		var dismissedID string
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM issues
			WHERE rule_id=? AND machine_uuid=? AND subject=? AND status='dismissed'
			LIMIT 1`,
			f.RuleID, machineUUID, f.Subject,
		).Scan(&dismissedID)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("store: lookup dismissed issue: %w", err)
		}

		// New issue.
		id := s.newID()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO issues
			    (id, rule_id, machine_uuid, subject, severity, message, fix,
			     status, first_seen_snapshot_id, last_seen_snapshot_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,'open',?,?,?,?)`,
			id, f.RuleID, machineUUID, f.Subject, f.Severity, f.Message, f.Fix,
			snapshotID, snapshotID, now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("store: insert issue: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

// FindingInput is the data from a rules engine finding used to upsert an issue.
type FindingInput struct {
	RuleID   string
	Subject  string
	Severity string
	Message  string
	Fix      string
}

// ListIssues returns issues for the given machine, optionally filtered by status.
// Pass status="" to return all statuses. Results are ordered newest-updated first.
func (s *DB) ListIssues(ctx context.Context, machineUUID string, status IssueStatus) ([]IssueRow, error) {
	q := `SELECT id, rule_id, machine_uuid, subject, severity, message, fix, status,
	             COALESCE(first_seen_snapshot_id,''), COALESCE(last_seen_snapshot_id,''),
	             created_at, updated_at
	      FROM issues WHERE machine_uuid=?`
	args := []any{machineUUID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, string(status))
	}
	q += ` ORDER BY updated_at DESC LIMIT 500`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssueRows(rows)
}

// UpdateIssueStatus transitions an issue to a new status.
func (s *DB) UpdateIssueStatus(ctx context.Context, id string, status IssueStatus) error {
	now := s.nowString()
	res, err := s.db.ExecContext(ctx,
		`UPDATE issues SET status=?, updated_at=? WHERE id=?`,
		string(status), now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordAppliedFix inserts a new applied_fixes row.
func (s *DB) RecordAppliedFix(ctx context.Context, fix AppliedFixInput) (string, error) {
	id := s.newID()
	now := s.nowString()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO applied_fixes (id, issue_id, applied_at, applied_by, command, output, exit_code)
		VALUES (?,?,?,?,?,?,?)`,
		id, fix.IssueID, now, fix.AppliedBy, fix.Command, fix.Output, fix.ExitCode,
	)
	if err != nil {
		return "", err
	}
	return id, nil
}

// AppliedFixInput carries the data for recording a fix attempt.
type AppliedFixInput struct {
	IssueID   string
	AppliedBy string
	Command   string
	Output    string
	ExitCode  int
}

// ListAppliedFixes returns the fix history for one issue.
func (s *DB) ListAppliedFixes(ctx context.Context, issueID string) ([]AppliedFixRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, issue_id, applied_at, applied_by, command, output, exit_code
		FROM applied_fixes WHERE issue_id=? ORDER BY applied_at DESC`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppliedFixRow
	for rows.Next() {
		var r AppliedFixRow
		var appliedAt string
		if err := rows.Scan(&r.ID, &r.IssueID, &appliedAt, &r.AppliedBy, &r.Command, &r.Output, &r.ExitCode); err != nil {
			return nil, err
		}
		r.AppliedAt, _ = time.Parse(time.RFC3339, appliedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanIssueRows(rows *sql.Rows) ([]IssueRow, error) {
	var out []IssueRow
	for rows.Next() {
		var r IssueRow
		var status, createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.RuleID, &r.MachineUUID, &r.Subject,
			&r.Severity, &r.Message, &r.Fix, &status,
			&r.FirstSeenSnapshotID, &r.LastSeenSnapshotID,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.Status = IssueStatus(status)
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		r.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *DB) nowString() string {
	clk := s.clock
	if clk == nil {
		clk = clock.System{}
	}
	return clk.Now().UTC().Format(time.RFC3339)
}

func (s *DB) newID() string {
	ids := s.ids
	if ids == nil {
		ids = idgen.UUID{}
	}
	return ids.Next()
}
