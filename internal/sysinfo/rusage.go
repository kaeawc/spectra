package sysinfo

import (
	"context"
	"errors"
	"sort"
	"time"
)

// ProcRusage is the subset of proc_pid_rusage we surface for per-pid energy
// accounting. Energy fields are in nanojoules as reported by the XNU
// scheduler (CLPC). Time fields are in nanoseconds.
type ProcRusage struct {
	PID              int    `json:"pid"`
	BilledEnergyNJ   uint64 `json:"billed_energy_nj"`
	ServicedEnergyNJ uint64 `json:"serviced_energy_nj"`
	EnergyNJ         uint64 `json:"energy_nj,omitempty"`  // V6 only: ri_energy_nj (total E+P core)
	PEnergyNJ        uint64 `json:"penergy_nj,omitempty"` // V6 only: ri_penergy_nj (P-core only)
	InterruptWakeups uint64 `json:"interrupt_wakeups"`
	PkgIdleWakeups   uint64 `json:"pkg_idle_wakeups"`
	UserNs           uint64 `json:"user_ns"`
	SystemNs         uint64 `json:"system_ns"`
	DiskBytesRead    uint64 `json:"disk_bytes_read"`
	DiskBytesWritten uint64 `json:"disk_bytes_written"`
}

// ErrRusageUnsupported is returned when proc_pid_rusage is unavailable for
// the platform or build (e.g. non-darwin or cgo disabled).
var ErrRusageUnsupported = errors.New("proc_pid_rusage unsupported on this platform")

// ErrRusagePermission is returned when the caller lacks permission to read
// rusage for a pid it does not own. Callers should skip the pid and continue.
var ErrRusagePermission = errors.New("proc_pid_rusage: permission denied")

// RusageReader reads a single pid's rusage snapshot.
type RusageReader func(pid int) (ProcRusage, error)

// EnergyDelta is the per-pid difference between two rusage snapshots taken
// Interval apart. All fields are deltas (after - before).
type EnergyDelta struct {
	PID              int           `json:"pid"`
	Command          string        `json:"command,omitempty"`
	Interval         time.Duration `json:"interval_ns"`
	BilledEnergyNJ   uint64        `json:"billed_energy_nj"`
	ServicedEnergyNJ uint64        `json:"serviced_energy_nj"`
	EnergyNJ         uint64        `json:"energy_nj,omitempty"`
	PEnergyNJ        uint64        `json:"penergy_nj,omitempty"`
	InterruptWakeups uint64        `json:"interrupt_wakeups"`
	PkgIdleWakeups   uint64        `json:"pkg_idle_wakeups"`
	UserNs           uint64        `json:"user_ns"`
	SystemNs         uint64        `json:"system_ns"`
	DiskBytesRead    uint64        `json:"disk_bytes_read"`
	DiskBytesWritten uint64        `json:"disk_bytes_written"`
}

// EnergySampler diffs two rusage snapshots taken Interval apart.
type EnergySampler struct {
	Interval time.Duration
	Reader   RusageReader // defaults to ReadRusage
	// Sleeper, if set, replaces the wall-clock wait between snapshots. Used by
	// tests to advance time deterministically.
	Sleeper func(ctx context.Context, d time.Duration) error
}

// Sample takes a before/after rusage snapshot for pids and returns per-pid
// deltas. Pids that fail with ErrRusagePermission in the "before" pass are
// dropped silently. Pids that disappear between passes are also dropped.
func (s EnergySampler) Sample(ctx context.Context, pids []int) ([]EnergyDelta, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reader := s.Reader
	if reader == nil {
		reader = ReadRusage
	}
	sleep := s.Sleeper
	if sleep == nil {
		sleep = defaultSleep
	}

	before := readAll(reader, pids)
	if err := sleep(ctx, s.Interval); err != nil {
		return nil, err
	}
	after := readAll(reader, pids)

	deltas := make([]EnergyDelta, 0, len(before))
	for pid, b := range before {
		a, ok := after[pid]
		if !ok {
			continue
		}
		deltas = append(deltas, diffOne(pid, b, a, s.Interval))
	}
	return deltas, nil
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func readAll(reader RusageReader, pids []int) map[int]ProcRusage {
	out := make(map[int]ProcRusage, len(pids))
	for _, pid := range pids {
		r, err := reader(pid)
		if err != nil {
			continue
		}
		out[pid] = r
	}
	return out
}

// sub returns a-b, clamped to zero. Counters can decrease across rusage
// versions or when a pid is recycled — clamping keeps the UI honest.
func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func diffOne(pid int, before, after ProcRusage, interval time.Duration) EnergyDelta {
	return EnergyDelta{
		PID:              pid,
		Interval:         interval,
		BilledEnergyNJ:   sub(after.BilledEnergyNJ, before.BilledEnergyNJ),
		ServicedEnergyNJ: sub(after.ServicedEnergyNJ, before.ServicedEnergyNJ),
		EnergyNJ:         sub(after.EnergyNJ, before.EnergyNJ),
		PEnergyNJ:        sub(after.PEnergyNJ, before.PEnergyNJ),
		InterruptWakeups: sub(after.InterruptWakeups, before.InterruptWakeups),
		PkgIdleWakeups:   sub(after.PkgIdleWakeups, before.PkgIdleWakeups),
		UserNs:           sub(after.UserNs, before.UserNs),
		SystemNs:         sub(after.SystemNs, before.SystemNs),
		DiskBytesRead:    sub(after.DiskBytesRead, before.DiskBytesRead),
		DiskBytesWritten: sub(after.DiskBytesWritten, before.DiskBytesWritten),
	}
}

// RankedEnergy returns deltas sorted by BilledEnergyNJ descending, truncated
// to top. Ties break on InterruptWakeups, then DiskBytesRead+Written, then
// PID for stability.
func RankedEnergy(deltas []EnergyDelta, top int) []EnergyDelta {
	out := make([]EnergyDelta, len(deltas))
	copy(out, deltas)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.BilledEnergyNJ != b.BilledEnergyNJ {
			return a.BilledEnergyNJ > b.BilledEnergyNJ
		}
		if a.InterruptWakeups != b.InterruptWakeups {
			return a.InterruptWakeups > b.InterruptWakeups
		}
		ai := a.DiskBytesRead + a.DiskBytesWritten
		bi := b.DiskBytesRead + b.DiskBytesWritten
		if ai != bi {
			return ai > bi
		}
		return a.PID < b.PID
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}
