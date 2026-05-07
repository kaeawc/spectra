// Package artifact records the lifecycle metadata for sensitive diagnostic
// artifacts such as heap dumps, JFR recordings, process samples, and pcaps.
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
)

const (
	KindHeapDump      = "heap_dump"
	KindJFRRecording  = "jfr"
	KindThreadDump    = "thread_dump"
	KindProcessSample = "process_sample"
	KindPacketCapture = "packet_capture"
	KindFlamegraph    = "flamegraph"

	SensitivityLow        = "low"
	SensitivityMedium     = "medium"
	SensitivityMediumHigh = "medium-high"
	SensitivityHigh       = "high"
	SensitivityVeryHigh   = "very-high"
)

const (
	PolicyConfirm = "confirm"
	PolicyDeny    = "deny"
	PolicyAllow   = "allow"
)

// Record describes one artifact that Spectra created or learned about.
type Record struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Sensitivity string            `json:"sensitivity"`
	Source      string            `json:"source,omitempty"`
	Command     string            `json:"command,omitempty"`
	Path        string            `json:"path,omitempty"`
	CacheKind   string            `json:"cache_kind,omitempty"`
	PID         int               `json:"pid,omitempty"`
	SizeBytes   int64             `json:"size_bytes,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Policy controls whether daemon RPC methods may create sensitive artifacts.
type Policy struct {
	Mode string `json:"mode"`
}

// Normalize returns p with defaults applied.
func (p Policy) Normalize() Policy {
	switch p.Mode {
	case PolicyDeny, PolicyAllow, PolicyConfirm:
		return p
	case "":
		return Policy{Mode: PolicyConfirm}
	default:
		return Policy{Mode: p.Mode}
	}
}

// Validate reports invalid policy modes.
func (p Policy) Validate() error {
	switch p.Normalize().Mode {
	case PolicyDeny, PolicyAllow, PolicyConfirm:
		return nil
	default:
		return fmt.Errorf("artifact policy %q must be one of: confirm, deny, allow", p.Mode)
	}
}

// Authorize checks whether rec may be created under this policy.
func (p Policy) Authorize(rec Record, confirmed bool) error {
	p = p.Normalize()
	switch p.Mode {
	case PolicyDeny:
		return fmt.Errorf("artifact policy denies %s artifacts", rec.Kind)
	case PolicyAllow:
		return nil
	case PolicyConfirm:
		if confirmed {
			return nil
		}
		return fmt.Errorf("%s requires confirm_sensitive=true under artifact policy %q", rec.Kind, p.Mode)
	default:
		return p.Validate()
	}
}

// Recorder records artifact lifecycle metadata. Use this interface in
// packages that create artifacts so tests can substitute a fake.
type Recorder interface {
	Record(ctx context.Context, rec Record) (Record, error)
}

// Store persists and retrieves artifact records.
type Store interface {
	Append(ctx context.Context, rec Record) error
	List(ctx context.Context) ([]Record, error)
}

// Manager stamps records with time, stable IDs, and best-effort file sizes.
type Manager struct {
	store Store
	clock clock.Clock
}

// NewManager returns a Recorder backed by store.
func NewManager(store Store, clk clock.Clock) *Manager {
	if clk == nil {
		clk = clock.Default
	}
	return &Manager{store: store, clock: clk}
}

// Record validates and persists rec.
func (m *Manager) Record(ctx context.Context, rec Record) (Record, error) {
	if rec.Kind == "" {
		return Record{}, fmt.Errorf("artifact: kind is required")
	}
	if rec.Sensitivity == "" {
		rec.Sensitivity = defaultSensitivity(rec.Kind)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = m.clock.Now().UTC()
	}
	if rec.SizeBytes == 0 && rec.Path != "" {
		if info, err := os.Stat(rec.Path); err == nil && !info.IsDir() {
			rec.SizeBytes = info.Size()
		}
	}
	if rec.ID == "" {
		rec.ID = stableID(rec)
	}
	if err := m.store.Append(ctx, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func defaultSensitivity(kind string) string {
	switch kind {
	case KindHeapDump:
		return SensitivityVeryHigh
	case KindPacketCapture:
		return SensitivityHigh
	case KindJFRRecording, KindThreadDump, KindFlamegraph:
		return SensitivityMediumHigh
	case KindProcessSample:
		return SensitivityMedium
	default:
		return SensitivityMedium
	}
}

func stableID(rec Record) string {
	h := sha256.New()
	write := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	write(rec.Kind)
	write(rec.Source)
	write(rec.Command)
	write(rec.Path)
	write(rec.CacheKind)
	write(fmt.Sprint(rec.PID))
	write(rec.CreatedAt.UTC().Format(time.RFC3339Nano))
	if len(rec.Metadata) > 0 {
		keys := make([]string, 0, len(rec.Metadata))
		for k := range rec.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			write(k)
			write(rec.Metadata[k])
		}
	}
	sum := h.Sum(nil)
	return "art-" + hex.EncodeToString(sum[:8])
}

// DefaultManifestPath returns the per-user artifact manifest path.
func DefaultManifestPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".spectra", "artifacts.json"), nil
}

// FakeRecorder is an in-memory Recorder for tests.
type FakeRecorder struct {
	mu      sync.Mutex
	Records []Record
	Err     error
}

// Record stores rec in memory unless Err is set.
func (f *FakeRecorder) Record(_ context.Context, rec Record) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Record{}, f.Err
	}
	f.Records = append(f.Records, rec)
	return rec, nil
}

// Snapshot returns a copy of recorded entries.
func (f *FakeRecorder) Snapshot() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Record, len(f.Records))
	copy(out, f.Records)
	return out
}
