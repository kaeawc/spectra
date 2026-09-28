package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type HostSampleRow struct {
	At   time.Time
	JSON []byte
}
type AlertRow struct {
	ID, Key, Severity, Title, Detail, State string
	FiredAt                                 time.Time
	ResolvedAt, AckedAt                     *time.Time
}

func (s *DB) SaveHostSample(ctx context.Context, at time.Time, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_samples(at_nano,sample_json) VALUES(?,?) ON CONFLICT(at_nano) DO UPDATE SET sample_json=excluded.sample_json`, at.UnixNano(), data)
	if err != nil {
		return fmt.Errorf("store: save host sample: %w", err)
	}
	return nil
}
func (s *DB) ListHostSamples(ctx context.Context, since time.Time, limit int) ([]HostSampleRow, error) {
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT at_nano,sample_json FROM host_samples WHERE at_nano>=? ORDER BY at_nano DESC LIMIT ?`, since.UnixNano(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: list host samples: %w", err)
	}
	defer rows.Close()
	var out []HostSampleRow
	for rows.Next() {
		var n int64
		var r HostSampleRow
		if err = rows.Scan(&n, &r.JSON); err != nil {
			return nil, fmt.Errorf("store: scan host sample: %w", err)
		}
		r.At = time.Unix(0, n).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *DB) PruneHostSamples(ctx context.Context, keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = 7
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM host_samples WHERE at_nano<?`, s.clock.Now().Add(-time.Duration(keepDays)*24*time.Hour).UnixNano())
	if err != nil {
		return 0, fmt.Errorf("store: prune host samples: %w", err)
	}
	return res.RowsAffected()
}
func (s *DB) UpsertAlert(ctx context.Context, a AlertRow) error {
	var resolved, acked any
	if a.ResolvedAt != nil {
		resolved = a.ResolvedAt.UnixNano()
	}
	if a.AckedAt != nil {
		acked = a.AckedAt.UnixNano()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO alerts(id,key,severity,title,detail,state,fired_at_nano,resolved_at_nano,acked_at_nano) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET severity=excluded.severity,title=excluded.title,detail=excluded.detail,state=excluded.state,resolved_at_nano=excluded.resolved_at_nano,acked_at_nano=excluded.acked_at_nano`, a.ID, a.Key, a.Severity, a.Title, a.Detail, a.State, a.FiredAt.UnixNano(), resolved, acked)
	if err != nil {
		return fmt.Errorf("store: upsert alert: %w", err)
	}
	return nil
}
func (s *DB) ListAlerts(ctx context.Context, state string, limit int) ([]AlertRow, error) {
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	q := `SELECT id,key,severity,title,detail,state,fired_at_nano,resolved_at_nano,acked_at_nano FROM alerts`
	var args []any
	if state != "" && state != "all" {
		q += ` WHERE state=?`
		args = append(args, state)
	}
	q += ` ORDER BY fired_at_nano DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list alerts: %w", err)
	}
	defer rows.Close()
	var out []AlertRow
	for rows.Next() {
		var a AlertRow
		var fired int64
		var resolved, acked sql.NullInt64
		if err = rows.Scan(&a.ID, &a.Key, &a.Severity, &a.Title, &a.Detail, &a.State, &fired, &resolved, &acked); err != nil {
			return nil, fmt.Errorf("store: scan alert: %w", err)
		}
		a.FiredAt = time.Unix(0, fired).UTC()
		if resolved.Valid {
			t := time.Unix(0, resolved.Int64).UTC()
			a.ResolvedAt = &t
		}
		if acked.Valid {
			t := time.Unix(0, acked.Int64).UTC()
			a.AckedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *DB) AckAlert(ctx context.Context, id string, at time.Time) (AlertRow, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE alerts SET acked_at_nano=? WHERE id=?`, at.UnixNano(), id)
	if err != nil {
		return AlertRow{}, fmt.Errorf("store: ack alert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return AlertRow{}, err
	}
	if n == 0 {
		return AlertRow{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,key,severity,title,detail,state,fired_at_nano,resolved_at_nano,acked_at_nano FROM alerts WHERE id=?`, id)
	if err != nil {
		return AlertRow{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return AlertRow{}, ErrNotFound
	}
	var a AlertRow
	var fired int64
	var resolved, acked sql.NullInt64
	if err = rows.Scan(&a.ID, &a.Key, &a.Severity, &a.Title, &a.Detail, &a.State, &fired, &resolved, &acked); err != nil {
		return AlertRow{}, err
	}
	a.FiredAt = time.Unix(0, fired).UTC()
	if resolved.Valid {
		t := time.Unix(0, resolved.Int64).UTC()
		a.ResolvedAt = &t
	}
	if acked.Valid {
		t := time.Unix(0, acked.Int64).UTC()
		a.AckedAt = &t
	}
	return a, nil
}
func (s *DB) PruneResolvedAlerts(ctx context.Context, keepDays int) (int64, error) {
	if keepDays <= 0 {
		keepDays = 30
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE state='resolved' AND resolved_at_nano<?`, s.clock.Now().Add(-time.Duration(keepDays)*24*time.Hour).UnixNano())
	if err != nil {
		return 0, fmt.Errorf("store: prune alerts: %w", err)
	}
	return res.RowsAffected()
}
