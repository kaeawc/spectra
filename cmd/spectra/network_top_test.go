package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/nettop"
	"github.com/kaeawc/spectra/internal/process"
)

// nettopAt renders one cumulative nettop sample.
func nettopAt(rows ...string) string {
	return ",bytes_in,bytes_out,\n" + strings.Join(rows, "\n") + "\n"
}

type topFake struct {
	clk     *clock.Fake
	outputs []string
	err     error
	calls   int
}

func (f *topFake) run(name string, _ ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.outputs[min(f.calls, len(f.outputs)-1)]
	f.calls++
	f.clk.Advance(40 * time.Millisecond) // one nettop sample's cost
	return []byte(out), nil
}

func topFakeDeps(kind hostos.Kind, outputs ...string) (networkTopDeps, *topFake) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	f := &topFake{clk: clk, outputs: outputs}
	return networkTopDeps{
		os:  kind,
		run: f.run,
		procs: func(context.Context) []process.Info {
			return []process.Info{
				{PID: 1, PPID: 0, Command: "launchd", User: "root"},
				{PID: 100, PPID: 1, Command: "Google Chrome Helper", User: "alice"},
				{PID: 101, PPID: 100, Command: "curl", User: "alice"},
				{PID: 200, PPID: 1, Command: "Slack", User: "alice"},
			}
		},
		conns: func() []netstate.Connection {
			return []netstate.Connection{
				{PID: 100, Proto: "udp", RemoteAddr: "8.8.8.8:53"},
				{PID: 100, Proto: "tcp", State: "established", RemoteAddr: "142.250.1.1:443"},
				{PID: 200, Proto: "tcp", State: "established", RemoteAddr: "52.1.2.3:443"},
			}
		},
		cwds:  func(context.Context, []int) map[int]string { return map[int]string{100: "/work/app"} },
		sleep: func(_ context.Context, d time.Duration) error { clk.Advance(d); return nil },
		clock: clk,
	}, f
}

var topSamples = []string{
	nettopAt("Google Chrome H.100,0,0,", "curl.101,0,0,", "Slack.200,0,0,"),
	nettopAt("Google Chrome H.100,1000,100,", "curl.101,10,0,", "Slack.200,50,50,"),
	nettopAt("Google Chrome H.100,3000,200,", "curl.101,20,0,", "Slack.200,100,100,"),
}

func TestRunNetworkTopText(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topSamples...)
	var out, errb bytes.Buffer
	code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s", "--interval", "1s"}, &out, &errb, deps)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"Sampling nettop every 1s for 2s", "Network top over 2.0s (3 samples, basis nettop)",
		"Google Chrome Helper", "alice", "142.250.1.1:443 8.8.8.8:53", "cwd /work/app", "Limitations:", "loopback",
		"PEAK OUT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if lines := strings.Count(s, " active\n"); lines != 2 {
		t.Errorf("per-interval lines = %d, want 2:\n%s", lines, s)
	}
	if strings.Index(s, "Google Chrome Helper") > strings.Index(s, "Slack ") {
		t.Errorf("not ranked by bytes:\n%s", s)
	}
}

func TestRunNetworkTopJSON(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topSamples...)
	var out, errb bytes.Buffer
	code := runNetworkTopWithIO(context.Background(), []string{"--json", "--duration", "2s", "--interval", "1s", "--top", "2"}, &out, &errb, deps)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	var got struct {
		DurationMS int64  `json:"duration_ms"`
		IntervalMS int64  `json:"interval_ms"`
		Samples    int    `json:"samples"`
		Basis      string `json:"basis"`
		Totals     struct {
			BytesIn         int64   `json:"bytes_in"`
			PeakTotalRate   float64 `json:"peak_total_bytes_per_sec"`
			ActiveProcesses int     `json:"active_processes"`
		} `json:"totals"`
		Processes []struct {
			PID        int      `json:"pid"`
			BytesIn    int64    `json:"bytes_in"`
			PeakInRate float64  `json:"peak_in_bytes_per_sec"`
			Remotes    []string `json:"remotes"`
		} `json:"processes"`
		Limitations []string `json:"limitations"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out.String())
	}
	if got.Basis != "nettop" || got.Samples != 3 || got.IntervalMS != 1000 || got.DurationMS != 2000 {
		t.Errorf("header = %+v", got)
	}
	if got.Totals.BytesIn != 3120 || got.Totals.ActiveProcesses != 3 || len(got.Processes) != 2 {
		t.Errorf("totals = %+v processes = %d", got.Totals, len(got.Processes))
	}
	if p := got.Processes[0]; p.PID != 100 || p.BytesIn != 3000 || p.PeakInRate < 1900 || len(p.Remotes) != 2 {
		t.Errorf("top process = %+v", p)
	}
	if len(got.Limitations) == 0 {
		t.Error("limitations empty")
	}
	if strings.Contains(out.String(), "Sampling") {
		t.Error("progress lines leaked into JSON")
	}
}

func TestRunNetworkTopLinux(t *testing.T) {
	ss := func(in int64) string {
		return fmt.Sprintf("ESTAB 0 0 10.0.0.5:51000 140.82.112.3:443 users:((\"git\",pid=200,fd=5))\n\t cubic bytes_acked:%d bytes_received:%d\n", in/10, in)
	}
	deps, _ := topFakeDeps(hostos.Linux, ss(1000), ss(5000), ss(9000))
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--json", "--duration", "2s"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	for _, want := range []string{`"basis": "ss-tcp"`, `"bytes_in": 8000`, `"140.82.112.3:443"`, "TCP only", "without root"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "52.1.2.3") {
		t.Errorf("connection listing overrode ss remotes:\n%s", s)
	}
}

func TestRunNetworkTopPIDSubtree(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topSamples...)
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--json", "--duration", "2s", "--pid", "100"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, `"pid": 101`) || strings.Contains(s, `"pid": 200`) || !strings.Contains(s, `"bytes_in": 3020`) {
		t.Errorf("subtree filter wrong:\n%s", s)
	}
	if !strings.Contains(s, "--pid: descendants") {
		t.Errorf("missing --pid limitation:\n%s", s)
	}

	errb.Reset()
	if code := runNetworkTopWithIO(context.Background(), []string{"--pid", "999"}, &out, &errb, deps); code != 1 || !strings.Contains(errb.String(), "999") {
		t.Errorf("unknown pid: code %d stderr %q", code, errb.String())
	}
}

func TestRunNetworkTopToolMissing(t *testing.T) {
	deps, f := topFakeDeps(hostos.Darwin, "")
	f.err = &exec.Error{Name: "nettop", Err: exec.ErrNotFound}
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "5s"}, &out, &errb, deps); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "nettop") || !strings.Contains(errb.String(), "not found") {
		t.Errorf("stderr = %q, want the tool named", errb.String())
	}
}

func TestRunNetworkTopEverySampleEmpty(t *testing.T) {
	deps, f := topFakeDeps(hostos.Darwin, ",bytes_in,bytes_out,\n")
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "3s"}, &out, &errb, deps); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "no usable sample from nettop") || f.calls != 4 {
		t.Errorf("stderr = %q calls = %d", errb.String(), f.calls)
	}
}

func TestRunNetworkTopCtrlC(t *testing.T) {
	deps, f := topFakeDeps(hostos.Darwin, topSamples...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sleeps := 0
	deps.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps++
		if sleeps == 2 {
			f.clk.Advance(d / 2)
			cancel()
			return ctx.Err()
		}
		f.clk.Advance(d)
		return nil
	}
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(ctx, []string{"--duration", "10s"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Network top over 1.0s (2 samples") {
		t.Errorf("summary missing actual duration:\n%s", out.String())
	}
}

func TestRunNetworkTopWriteError(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topSamples...)
	var errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s"}, failWriter{}, &errb, deps); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "write output") {
		t.Errorf("stderr = %q", errb.String())
	}
	rep := nettop.Report{Samples: 2, Processes: []nettop.Process{{PID: 1, Command: "x", BytesIn: 1}}}
	if err := printNetworkTopReport(failWriter{}, rep); err == nil {
		t.Error("printer swallowed the write error")
	}
}

func TestRunNetworkTopUsage(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topSamples...)
	for _, args := range [][]string{
		{"--bogus"}, {"extra"}, {"--interval", "0"}, {"--duration", "-1s"}, {"--top", "0"}, {"--pid", "-3"},
	} {
		var out, errb bytes.Buffer
		if code := runNetworkTopWithIO(context.Background(), args, &out, &errb, deps); code != 2 {
			t.Errorf("%v: code = %d, want 2", args, code)
		}
	}
}

func TestRunNetworkTopUnsupportedOS(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Unknown, topSamples...)
	restore := hostos.SetForTest(hostos.Kind(99))
	defer restore()
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), nil, &out, &errb, deps); code != 1 || !strings.Contains(errb.String(), "unsupported") {
		t.Errorf("code = %d stderr = %q", code, errb.String())
	}
}

func TestHumanRate(t *testing.T) {
	for r, want := range map[float64]string{0: "0 B/s", 999: "999 B/s", 2048: "2.0 KB/s", 3 << 20: "3.0 MB/s", 5 << 30: "5.0 GB/s"} {
		if got := humanRate(r); got != want {
			t.Errorf("humanRate(%v) = %q, want %q", r, got, want)
		}
	}
}
