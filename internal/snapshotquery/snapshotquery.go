// Package snapshotquery answers read-only questions about stored snapshots:
// the list `spectra snapshot list` prints and the diff `spectra snapshot diff`
// prints. The CLI and the daemon both build their JSON here, so the two
// cannot drift.
package snapshotquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/diff"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

// LiveID names a snapshot captured on the fly instead of loaded from the store.
const LiveID = "live"

// Reader is the read side of the snapshot store; *store.DB satisfies it.
type Reader interface {
	ListSnapshots(ctx context.Context, machineUUID string) ([]store.SnapshotRow, error)
	GetSnapshot(ctx context.Context, id string) (store.SnapshotRow, error)
	GetSnapshotJSON(ctx context.Context, id string) ([]byte, error)
	MostRecentSnapshotOlderThan(ctx context.Context, cutoff time.Time) (*snapshot.Snapshot, error)
}

// List returns the stored snapshot rows, newest first, keeping only kind when
// it is set. The result is never nil, so it encodes as an array.
func List(ctx context.Context, r Reader, kind string) ([]store.SnapshotRow, error) {
	rows, err := r.ListSnapshots(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]store.SnapshotRow, 0, len(rows))
	for _, row := range rows {
		if kind == "" || row.Kind == kind {
			out = append(out, row)
		}
	}
	return out, nil
}

// ValidKind reports whether kind is a list filter List understands.
func ValidKind(kind string) bool {
	switch snapshot.Kind(kind) {
	case "", snapshot.KindLive, snapshot.KindBaseline:
		return true
	}
	return false
}

// DiffOptions select the two snapshots to compare, in one of three forms:
// A and B (IDs or LiveID); Baseline, where A names a baseline by name or ID
// (empty for the newest) and B defaults to LiveID; or Since, which compares
// the newest snapshot older than Since against a live one.
type DiffOptions struct {
	A, B     string
	Baseline bool
	Since    time.Duration
}

// Validate reports a malformed combination.
func (o DiffOptions) Validate() error {
	switch {
	case o.Since < 0:
		return errors.New("since must be positive")
	case o.Since > 0:
		if o.A != "" || o.B != "" || o.Baseline {
			return errors.New("since cannot be combined with explicit snapshot IDs")
		}
	case o.Baseline:
	case o.A == "" || o.B == "":
		return errors.New("two snapshot IDs (or live) are required")
	}
	return nil
}

// DiffOptionsFromArgs maps `spectra snapshot diff` arguments onto options:
// `<a> <b>`, `baseline [name|id] [live|id]`, or none with since.
func DiffOptionsFromArgs(args []string, since time.Duration) (DiffOptions, error) {
	if since != 0 {
		if len(args) != 0 {
			return DiffOptions{}, errors.New("since cannot be combined with explicit snapshot IDs")
		}
		return DiffOptions{Since: since}, nil
	}
	if len(args) > 0 && args[0] == "baseline" {
		if len(args) > 3 {
			return DiffOptions{}, errors.New("invalid baseline diff operands")
		}
		o := DiffOptions{Baseline: true}
		if len(args) > 1 {
			o.A = args[1]
		}
		if len(args) > 2 {
			o.B = args[2]
		}
		return o, nil
	}
	if len(args) != 2 {
		return DiffOptions{}, errors.New("invalid diff operands")
	}
	return DiffOptions{A: args[0], B: args[1]}, nil
}

// DiffDeps supply the live capture and the clock.
type DiffDeps struct {
	// Live captures a fresh snapshot; it should honor ctx.
	Live func(ctx context.Context) *snapshot.Snapshot
	Now  func() time.Time
}

// Diff resolves the two snapshots o names and compares them.
func Diff(ctx context.Context, r Reader, o DiffOptions, d DiffDeps) (diff.Result, error) {
	if err := o.Validate(); err != nil {
		return diff.Result{}, err
	}
	var a, b *snapshot.Snapshot
	var err error
	switch {
	case o.Since > 0:
		a, b, err = sinceOperands(ctx, r, o.Since, d)
	case o.Baseline:
		a, b, err = baselineOperands(ctx, r, o.A, o.B, d)
	default:
		a, b, err = idOperands(ctx, r, o.A, o.B, d)
	}
	if err != nil {
		return diff.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return diff.Result{}, err
	}
	return diff.Compare(*a, *b), nil
}

func idOperands(ctx context.Context, r Reader, idA, idB string, d DiffDeps) (*snapshot.Snapshot, *snapshot.Snapshot, error) {
	a, err := Resolve(ctx, r, idA, d.Live)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot %q: %w", idA, err)
	}
	b, err := Resolve(ctx, r, idB, d.Live)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot %q: %w", idB, err)
	}
	return a, b, nil
}

func sinceOperands(ctx context.Context, r Reader, since time.Duration, d DiffDeps) (*snapshot.Snapshot, *snapshot.Snapshot, error) {
	cutoff := d.Now().UTC().Add(-since)
	base, err := r.MostRecentSnapshotOlderThan(ctx, cutoff)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, fmt.Errorf("no stored snapshot older than %s exists; run `spectra snapshot create`", since)
	}
	if err != nil {
		return nil, nil, err
	}
	return base, d.Live(ctx), nil
}

func baselineOperands(ctx context.Context, r Reader, ref, other string, d DiffDeps) (*snapshot.Snapshot, *snapshot.Snapshot, error) {
	if other == "" {
		other = LiveID
	}
	a, err := ResolveBaseline(ctx, r, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("baseline %q: %w", baselineDisplayName(ref), err)
	}
	b, err := Resolve(ctx, r, other, d.Live)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot %q: %w", other, err)
	}
	return a, b, nil
}

// Resolve loads a snapshot by ID, or captures one with live when id is LiveID.
func Resolve(ctx context.Context, r Reader, id string, live func(context.Context) *snapshot.Snapshot) (*snapshot.Snapshot, error) {
	if id == LiveID {
		return live(ctx), nil
	}
	return Load(ctx, r, id)
}

// ResolveBaseline finds a baseline by ID or name, or the newest one when ref
// is empty.
func ResolveBaseline(ctx context.Context, r Reader, ref string) (*snapshot.Snapshot, error) {
	if ref != "" {
		row, err := r.GetSnapshot(ctx, ref)
		if err == nil {
			if row.Kind != string(snapshot.KindBaseline) {
				return nil, fmt.Errorf("snapshot is %q, not baseline", row.Kind)
			}
			return Load(ctx, r, row.ID)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}

	rows, err := r.ListSnapshots(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Kind != string(snapshot.KindBaseline) {
			continue
		}
		if ref == "" || row.Name == ref {
			return Load(ctx, r, row.ID)
		}
	}
	if ref == "" {
		return nil, fmt.Errorf("no baselines stored")
	}
	return nil, fmt.Errorf("not found")
}

func baselineDisplayName(ref string) string {
	if ref == "" {
		return "latest"
	}
	return ref
}

// Load retrieves the full snapshot JSON for id. It fails when the snapshot
// is missing or was saved without a JSON blob (pre-v0.8 rows).
func Load(ctx context.Context, r Reader, id string) (*snapshot.Snapshot, error) {
	raw, err := r.GetSnapshotJSON(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("not found")
	}
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("snapshot exists but has no JSON blob (was it taken before this version?)")
	}
	var s snapshot.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &s, nil
}
