package hostload

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
)

func statLine(pid int, cpuTicks, start int64) string {
	// utime=cpuTicks stime=0; fields 3..24 after "(comm)".
	return fmt.Sprintf("%d (w) S 1 1 1 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 %d 0 0\n", pid, cpuTicks, start)
}

type fakeProc struct {
	files map[string]string
	now   time.Time
	// at maps the collect call index to the procs it returns.
	rounds [][]process.Info
	calls  int
	// onSleep mutates files while the clock is "sleeping".
	onSleep func()
}

func (f *fakeProc) sampler(os hostos.Kind) Sampler {
	return Sampler{
		Host: Host{OS: os, ReadFile: func(p string) ([]byte, error) {
			if s, ok := f.files[p]; ok {
				return []byte(s), nil
			}
			return nil, errors.New("gone")
		}},
		Collect: func(context.Context) []process.Info {
			r := f.rounds[min(f.calls, len(f.rounds)-1)]
			f.calls++
			return append([]process.Info(nil), r...)
		},
		Now: func() time.Time { return f.now },
		Sleep: func(_ context.Context, d time.Duration) {
			if f.onSleep != nil {
				f.onSleep()
			}
			f.now = f.now.Add(d)
		},
	}
}

func byPID(procs []process.Info) map[int]process.Info {
	m := map[int]process.Info{}
	for _, p := range procs {
		m[p.PID] = p
	}
	return m
}

func TestSampleRecentOutranksFormerlyBusy(t *testing.T) {
	// pid 1: day-old worker, lifetime 0.07%, burned 50 ticks in the window.
	// pid 2: formerly busy sleeper, lifetime 90%, no ticks in the window.
	f := &fakeProc{
		now: time.Unix(1000, 0),
		files: map[string]string{
			"/proc/1/stat": statLine(1, 1000, 5),
			"/proc/2/stat": statLine(2, 9000, 6),
		},
		rounds: [][]process.Info{{{PID: 1, CPUPct: 0.07}, {PID: 2, CPUPct: 90}}},
	}
	f.onSleep = func() { f.files["/proc/1/stat"] = statLine(1, 1050, 5) }
	procs, window, _ := f.sampler(hostos.Linux).Sample(context.Background(), 500*time.Millisecond)
	if window != 500*time.Millisecond {
		t.Fatalf("window = %v", window)
	}
	m := byPID(procs)
	if m[1].CPUPct != 100 || m[2].CPUPct != 0 {
		t.Fatalf("cpu = %v / %v, want 100 / 0", m[1].CPUPct, m[2].CPUPct)
	}
	an := Analyze(procs, map[int]string{}, Options{TopN: 2})
	if an.TopCPU[0].PID != 1 {
		t.Fatalf("top = %+v", an.TopCPU)
	}
	if an.Cause.Kind == CauseNone || an.Cause.CPUPct != 100 {
		t.Fatalf("cause = %+v", an.Cause)
	}
}

func TestSampleSkipsReusedPID(t *testing.T) {
	f := &fakeProc{
		now:    time.Unix(1000, 0),
		files:  map[string]string{"/proc/1/stat": statLine(1, 5000, 5)},
		rounds: [][]process.Info{{{PID: 1, CPUPct: 42}}},
	}
	// Same pid, different start time and a lower counter: a new process.
	f.onSleep = func() { f.files["/proc/1/stat"] = statLine(1, 10, 99) }
	procs, window, _ := f.sampler(hostos.Linux).Sample(context.Background(), time.Second)
	if window != 0 || procs[0].CPUPct != 42 {
		t.Fatalf("window=%v cpu=%v, want collector value kept", window, procs[0].CPUPct)
	}
}

func TestSampleGoneDroppedNewKept(t *testing.T) {
	f := &fakeProc{
		now: time.Unix(1000, 0),
		files: map[string]string{
			"/proc/1/stat": statLine(1, 100, 5),
			"/proc/2/stat": statLine(2, 100, 6),
		},
		rounds: [][]process.Info{
			{{PID: 1, CPUPct: 1}, {PID: 2, CPUPct: 2}},
			{{PID: 1, CPUPct: 1}, {PID: 3, CPUPct: 7}},
		},
	}
	f.onSleep = func() {
		f.files["/proc/1/stat"] = statLine(1, 110, 5)
		delete(f.files, "/proc/2/stat")
		f.files["/proc/3/stat"] = statLine(3, 500, 8)
	}
	procs, window, _ := f.sampler(hostos.Linux).Sample(context.Background(), time.Second)
	m := byPID(procs)
	if _, ok := m[2]; ok || len(procs) != 2 {
		t.Fatalf("exited pid 2 should be dropped: %+v", procs)
	}
	if m[3].CPUPct != 7 {
		t.Fatalf("new pid should keep collector value, got %v", m[3].CPUPct)
	}
	if m[1].CPUPct != 10 || window != time.Second {
		t.Fatalf("pid1 cpu=%v window=%v", m[1].CPUPct, window)
	}
}

func TestSampleDisabledAndMacOS(t *testing.T) {
	for name, tc := range map[string]struct {
		os       hostos.Kind
		interval time.Duration
	}{"zero": {hostos.Linux, 0}, "darwin": {hostos.Darwin, time.Second}} {
		t.Run(name, func(t *testing.T) {
			f := &fakeProc{
				now:    time.Unix(1000, 0),
				files:  map[string]string{"/proc/1/stat": statLine(1, 100, 5)},
				rounds: [][]process.Info{{{PID: 1, CPUPct: 33}}},
			}
			f.onSleep = func() { f.files["/proc/1/stat"] = statLine(1, 900, 5) }
			procs, window, _ := f.sampler(tc.os).Sample(context.Background(), tc.interval)
			if window != 0 || procs[0].CPUPct != 33 || f.calls != 1 {
				t.Fatalf("window=%v cpu=%v calls=%d", window, procs[0].CPUPct, f.calls)
			}
		})
	}
}

func TestSampleBasis(t *testing.T) {
	mk := func() *fakeProc {
		return &fakeProc{
			now:    time.Unix(1000, 0),
			files:  map[string]string{"/proc/1/stat": statLine(1, 100, 5)},
			rounds: [][]process.Info{{{PID: 1, CPUPct: 33}}},
		}
	}
	ctx := context.Background()
	if _, _, b := mk().sampler(hostos.Darwin).Sample(ctx, time.Second); b != BasisRecent {
		t.Fatalf("darwin basis = %s", b)
	}
	if _, _, b := mk().sampler(hostos.Linux).Sample(ctx, 0); b != BasisLifetime {
		t.Fatalf("linux --sample 0 basis = %s", b)
	}
	f := mk()
	f.onSleep = func() { f.files["/proc/1/stat"] = statLine(1, 150, 5) }
	if _, w, b := f.sampler(hostos.Linux).Sample(ctx, time.Second); b != BasisWindow || w != time.Second {
		t.Fatalf("linux basis = %s window=%v", b, w)
	}
	// Nothing diffable: falls back to lifetime.
	u := mk()
	u.onSleep = func() { delete(u.files, "/proc/1/stat") }
	if _, w, b := u.sampler(hostos.Linux).Sample(ctx, time.Second); b != BasisLifetime || w != 0 {
		t.Fatalf("undiffable basis = %s window=%v", b, w)
	}
}

func TestSampleStartedInsideWindow(t *testing.T) {
	t0 := time.Unix(1000, 0)
	f := &fakeProc{
		now: t0,
		files: map[string]string{
			"/proc/1/stat": statLine(1, 1000, 5),
			"/proc/2/stat": statLine(2, 1, 6), // unreadable in first reading, old
		},
		rounds: [][]process.Info{
			{{PID: 1, CPUPct: 1}},
			{
				{PID: 1, CPUPct: 1},
				// Lifetime of ~0.02s: collector says 150%.
				{PID: 3, CPUPct: 150, StartTime: t0.Add(-500 * time.Millisecond)},
				{PID: 2, CPUPct: 12, StartTime: t0.Add(-time.Hour)},
			},
		},
	}
	f.onSleep = func() {
		f.files["/proc/1/stat"] = statLine(1, 1090, 5) // 90 ticks / 1s = 90%
		f.files["/proc/3/stat"] = statLine(3, 2, 8)    // 2 ticks over 1s = 2%
	}
	procs, _, _ := f.sampler(hostos.Linux).Sample(context.Background(), time.Second)
	m := byPID(procs)
	if m[3].CPUPct != 2 {
		t.Fatalf("new pid cpu = %v, want 2", m[3].CPUPct)
	}
	if m[2].CPUPct != 12 {
		t.Fatalf("old unreadable pid should keep collector value, got %v", m[2].CPUPct)
	}
	an := Analyze(procs, map[int]string{}, Options{TopN: 1})
	if an.TopCPU[0].PID != 1 {
		t.Fatalf("steady 90%% process should outrank: %+v", an.TopCPU)
	}
}
