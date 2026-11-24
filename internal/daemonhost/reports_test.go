package daemonhost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/process"
)

var reportsNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func fakeReports(sampled *time.Duration, gotPid *int) *Reports {
	procs := []process.Info{
		{PID: 1, Command: "init"},
		{PID: 20, PPID: 1, Command: "slot", StartTime: reportsNow.Add(-time.Hour)},
		{PID: 30, PPID: 1, Command: "caller", StartTime: reportsNow.Add(-time.Hour)},
	}
	return &Reports{
		Load: func() hostload.ReportDeps {
			return hostload.ReportDeps{
				Host: hostload.Host{
					OS: hostos.Linux,
					ReadFile: func(p string) ([]byte, error) {
						switch p {
						case "/proc/loadavg":
							return []byte("1.00 1 1 1/1 1"), nil
						case "/proc/meminfo":
							return []byte("MemAvailable: 16777216 kB\n"), nil
						}
						return nil, errors.New("missing")
					},
					Readlink: func(string) (string, error) { return "", errors.New("denied") },
					NumCPU:   func() int { return 4 },
				},
				Procs: func(context.Context) []process.Info { return procs },
				Now:   func() time.Time { return reportsNow },
			}
		},
		Idle: func() idle.ReportDeps {
			return idle.ReportDeps{
				Sample: func(_ context.Context, w time.Duration) (idle.Sample, error) {
					*sampled = w
					return idle.Sample{Procs: procs, CPU: map[int]float64{20: 0, 30: 0}, Window: w, Basis: idle.BasisWindow}, nil
				},
				Cwds: func(context.Context, []process.Info) (map[int]string, bool, error) {
					return map[int]string{20: "/work/lane", 30: "/work/lane"}, false, nil
				},
				Waits: func(context.Context, []int) map[int]*idle.Wait { return nil },
				Stat:  func(string) (os.FileInfo, error) { return os.Stat(os.TempDir()) },
				Now:   func() time.Time { return reportsNow },
				Pid: func() int {
					*gotPid = os.Getpid()
					return os.Getpid()
				},
				Abs:          func(s string) (string, error) { return s, nil },
				EvalSymlinks: func(s string) (string, error) { return s, nil },
			}
		},
	}
}

func callReport(t *testing.T, h daemon.Handler, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return h(context.Background(), &daemon.Request{Params: raw})
}

func TestLoadReportMatchesLocalBuild(t *testing.T) {
	var w time.Duration
	var pid int
	r := fakeReports(&w, &pid)
	opts := hostload.DefaultReportOptions()
	opts.Sample = 0
	opts.Roots = []string{"/work"}
	got, err := callReport(t, r.rpcLoad, LoadParamsFrom(opts))
	if err != nil {
		t.Fatal(err)
	}
	want, err := hostload.BuildReport(context.Background(), opts, r.Load())
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("rpc report differs from a local build:\n got %s\nwant %s", gj, wj)
	}
}

func TestReportParamsRejected(t *testing.T) {
	var w time.Duration
	var pid int
	r := fakeReports(&w, &pid)
	load := LoadParamsFrom(hostload.DefaultReportOptions())
	relLoad := load
	relLoad.Roots = []string{"work"}
	badLoad := load
	badLoad.Top = 0
	idleOK := IdleParamsFrom(idle.DefaultReportOptions(), 30)
	tests := []struct {
		name   string
		h      daemon.Handler
		params any
		want   string
	}{
		{"load without params", r.rpcLoad, nil, "params are required"},
		{"load relative root", r.rpcLoad, relLoad, "must be absolute"},
		{"load invalid option", r.rpcLoad, badLoad, "invalid load options"},
		{"idle without scope", r.rpcIdle, idleOK, "roots or pids are required"},
		{"idle relative root", r.rpcIdle, func() IdleParams { p := idleOK; p.Roots = []string{"lane"}; return p }(), "must be absolute"},
		{"idle long timeout", r.rpcIdle, func() IdleParams {
			p := idleOK
			p.PIDs = []int{20}
			p.TimeoutMS = int64(time.Hour / time.Millisecond)
			return p
		}(), "at most"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callReport(t, tc.h, tc.params)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestIdleReportExcludesCaller(t *testing.T) {
	var w time.Duration
	var pid int
	r := fakeReports(&w, &pid)
	opts := idle.DefaultReportOptions()
	opts.Roots = []string{"/work"}
	opts.Window = 2 * time.Second
	got, err := callReport(t, r.rpcIdle, IdleParamsFrom(opts, 30))
	if err != nil {
		t.Fatal(err)
	}
	rep := got.(idle.Report)
	if w != 2*time.Second {
		t.Fatalf("sampled %v, want the requested window", w)
	}
	var holders []int
	for _, h := range rep.Result.Holders {
		holders = append(holders, h.PID)
	}
	if len(holders) != 1 || holders[0] != 20 {
		t.Fatalf("holders = %v, want only 20 (30 is the caller)", holders)
	}
	if pid != 0 {
		t.Fatal("used the daemon's own pid instead of the caller's")
	}
}

func TestLoadReportUsesTrackerFromNewReports(t *testing.T) {
	r := NewReports()
	if r.Load().Baseline == nil {
		t.Fatal("load deps carry no tracker baseline")
	}
}

func TestLoadReportReadsCallerCgroup(t *testing.T) {
	var w time.Duration
	var pid int
	r := fakeReports(&w, &pid)
	base := r.Load
	var read []string
	r.Load = func() hostload.ReportDeps {
		d := base()
		rf := d.Host.ReadFile
		d.Host.ReadFile = func(p string) ([]byte, error) {
			read = append(read, p)
			return rf(p)
		}
		return d
	}
	p := LoadParamsFrom(hostload.ReportOptions{Top: 5, MaxLoadPerCPU: 1.5, MinFreeGB: 8, BackoffFreeGB: 4, BootWindow: time.Minute})
	p.CallerPID = 4242
	if _, err := callReport(t, r.rpcLoad, p); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(read, "/proc/4242/cgroup") || slices.Contains(read, "/proc/self/cgroup") {
		t.Fatalf("read %v, want the caller's cgroup", read)
	}
}
