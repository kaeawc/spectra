package sysinfo

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestEnergySamplerHappyPath(t *testing.T) {
	calls := 0
	reader := func(pid int) (ProcRusage, error) {
		calls++
		base := ProcRusage{PID: pid, BilledEnergyNJ: 1000, EnergyNJ: 1100, InterruptWakeups: 50}
		if calls > 2 {
			base.BilledEnergyNJ += uint64(pid) * 1_000_000
			base.EnergyNJ += uint64(pid) * 1_100_000
			base.InterruptWakeups += uint64(pid) * 100
			base.UserNs += uint64(pid) * 500_000
		}
		return base, nil
	}
	s := EnergySampler{Interval: 100 * time.Millisecond, Reader: reader, Sleeper: noSleep}
	deltas, err := s.Sample(context.Background(), []int{1, 2})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if len(deltas) != 2 {
		t.Fatalf("len(deltas) = %d, want 2", len(deltas))
	}
	ranked := RankedEnergy(deltas, 0)
	if ranked[0].PID != 2 || ranked[1].PID != 1 {
		t.Fatalf("rank order = %v, want pid 2 then 1", []int{ranked[0].PID, ranked[1].PID})
	}
	if ranked[0].BilledEnergyNJ != 2_000_000 {
		t.Fatalf("pid 2 billed = %d, want 2_000_000", ranked[0].BilledEnergyNJ)
	}
	if ranked[0].Interval != 100*time.Millisecond {
		t.Fatalf("interval = %v, want 100ms", ranked[0].Interval)
	}
}

func TestEnergySamplerZeroDelta(t *testing.T) {
	frozen := ProcRusage{PID: 7, BilledEnergyNJ: 999_999, InterruptWakeups: 10}
	reader := func(pid int) (ProcRusage, error) { return frozen, nil }
	s := EnergySampler{Interval: time.Millisecond, Reader: reader, Sleeper: noSleep}
	deltas, err := s.Sample(context.Background(), []int{7})
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 {
		t.Fatalf("len(deltas) = %d, want 1", len(deltas))
	}
	d := deltas[0]
	if d.BilledEnergyNJ != 0 || d.InterruptWakeups != 0 || d.EnergyNJ != 0 {
		t.Fatalf("expected all-zero delta for sleeping pid, got %+v", d)
	}
}

func TestEnergySamplerEPermSkipped(t *testing.T) {
	reader := func(pid int) (ProcRusage, error) {
		if pid == 1 {
			return ProcRusage{}, ErrRusagePermission
		}
		return ProcRusage{PID: pid, BilledEnergyNJ: 42}, nil
	}
	s := EnergySampler{Interval: time.Millisecond, Reader: reader, Sleeper: noSleep}
	deltas, err := s.Sample(context.Background(), []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0].PID != 2 {
		t.Fatalf("expected only pid 2 to survive EPERM skip, got %+v", deltas)
	}
}

func TestEnergySamplerCtxCancel(t *testing.T) {
	reader := func(pid int) (ProcRusage, error) { return ProcRusage{PID: pid}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := EnergySampler{Interval: 5 * time.Second, Reader: reader}
	_, err := s.Sample(ctx, []int{1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRankedEnergyTopN(t *testing.T) {
	in := []EnergyDelta{
		{PID: 1, BilledEnergyNJ: 100},
		{PID: 2, BilledEnergyNJ: 300},
		{PID: 3, BilledEnergyNJ: 200},
		{PID: 4, BilledEnergyNJ: 300, InterruptWakeups: 99},
	}
	got := RankedEnergy(in, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].PID != 4 || got[1].PID != 2 {
		t.Fatalf("top 2 = %v, want [4 2] (4 wins tiebreak on wakeups)", []int{got[0].PID, got[1].PID})
	}
}

func TestReadRusageSelf(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("proc_pid_rusage only available on darwin")
	}
	pid := os.Getpid()
	r, err := ReadRusage(pid)
	if errors.Is(err, ErrRusageUnsupported) {
		t.Skip("cgo disabled in this build")
	}
	if err != nil {
		t.Fatalf("ReadRusage(self): %v", err)
	}
	if r.PID != pid {
		t.Fatalf("PID = %d, want %d", r.PID, pid)
	}
	if r.UserNs == 0 && r.SystemNs == 0 {
		t.Fatalf("expected non-zero CPU time for self, got %+v", r)
	}
}

func noSleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}
