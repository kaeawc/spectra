package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/nettop"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// nettopRun renders one `nettop -d -L 2` run: a first block of open-flow
// totals (bogus here, so using it would show) and the interval's deltas.
func nettopRun(rows ...string) string {
	var first []string
	for _, r := range rows {
		name, _, _ := strings.Cut(r, ",")
		first = append(first, name+",999999,999999,")
	}
	return ",bytes_in,bytes_out,\n" + strings.Join(first, "\n") + "\n,bytes_in,bytes_out,\n" + strings.Join(rows, "\n") + "\n"
}

type topFake struct {
	clk     *clock.Fake
	outputs []string
	err     error
	calls   int
	secs    []string
	// during, if set, runs inside a nettop run before it returns.
	during func(call int) error
}

// ss is one Linux ss sample.
func (f *topFake) ss() (proc.Result, error) {
	out := f.outputs[min(f.calls, len(f.outputs)-1)]
	f.calls++
	f.clk.Advance(40 * time.Millisecond) // one ss sample's cost
	return proc.Result{Stdout: []byte(out)}, nil
}

// Run is the proc.Runner: one ss sample on Linux, one nettop run lasting
// its -s delay on macOS.
func (f *topFake) Run(_ context.Context, cmd proc.Cmd) (proc.Result, error) {
	if f.err != nil {
		return proc.Result{ExitCode: -1}, f.err
	}
	if cmd.Name == "ss" {
		return f.ss()
	}
	call := f.calls
	f.calls++
	if f.during != nil {
		if err := f.during(call); err != nil {
			return proc.Result{ExitCode: -1}, err
		}
	}
	secs := "1"
	for i, a := range cmd.Args {
		if a == "-s" && i+1 < len(cmd.Args) {
			secs = cmd.Args[i+1]
		}
	}
	f.secs = append(f.secs, secs)
	n, _ := strconv.Atoi(secs)
	f.clk.Advance(time.Duration(n) * time.Second)
	return proc.Result{Stdout: []byte(f.outputs[min(call, len(f.outputs)-1)])}, nil
}

func topFakeDeps(kind hostos.Kind, outputs ...string) (networkTopDeps, *topFake) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	f := &topFake{clk: clk, outputs: outputs}
	return networkTopDeps{
		os:     kind,
		runner: f,
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

// topRuns are two one-second nettop runs; as cumulative totals they are
// Chrome 1000->3000, curl 10->20, Slack 50->100 bytes in.
var topRuns = []string{
	nettopRun("Google Chrome H.100,1000,100,", "curl.101,10,0,", "Slack.200,50,50,"),
	nettopRun("Google Chrome H.100,2000,100,", "curl.101,10,0,", "Slack.200,50,50,"),
}

func TestRunNetworkTopText(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topRuns...)
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
	deps, _ := topFakeDeps(hostos.Darwin, topRuns...)
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

// Per-interval lines count the subtree alive at the start; the summary also
// counts descendants found at the end, and every output says so.
func TestRunNetworkTopPIDScopeIsStated(t *testing.T) {
	runs := []string{
		nettopRun("Google Chrome H.100,1000,0,", "curl.101,10,0,", "Slack.200,50,0,"),
		nettopRun("Google Chrome H.100,2000,0,", "curl.101,10,0,", "node.102,5000,0,"),
	}
	deps, _ := topFakeDeps(hostos.Darwin, runs...)
	reads := 0
	base := deps.procs
	deps.procs = func(ctx context.Context) []process.Info {
		reads++
		ps := base(ctx)
		if reads > 1 { // a child born inside the window
			ps = append(ps, process.Info{PID: 102, PPID: 100, Command: "node", User: "alice"})
		}
		return ps
	}
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s", "--pid", "100"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "in     1010 B/s") || !strings.Contains(s, "in     2.0 KB/s") {
		t.Errorf("interval lines should sum the start subtree (100, 101) only:\n%s", s)
	}
	if !strings.Contains(s, "node") || !strings.Contains(s, "total: in 8 KB") {
		t.Errorf("summary should include the child born in the window:\n%s", s)
	}
	if strings.Count(s, pidScopeNote) != 2 {
		t.Errorf("scope note should head the interval lines and appear in limitations:\n%s", s)
	}

	reads = 0
	out.Reset()
	if code := runNetworkTopWithIO(context.Background(), []string{"--json", "--duration", "2s", "--pid", "100"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), pidScopeNote) || !strings.Contains(out.String(), `"pid": 102`) {
		t.Errorf("json:\n%s", out.String())
	}
	var help bytes.Buffer
	parseNetworkTopFlags([]string{"-h"}, &help)
	if !strings.Contains(help.String(), "descendants alive at the start") {
		t.Errorf("help:\n%s", help.String())
	}
}

func TestRunNetworkTopPIDSubtree(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topRuns...)
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--json", "--duration", "2s", "--pid", "100"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, `"pid": 101`) || strings.Contains(s, `"pid": 200`) || !strings.Contains(s, `"bytes_in": 3020`) {
		t.Errorf("subtree filter wrong:\n%s", s)
	}
	if !strings.Contains(s, pidScopeNote) {
		t.Errorf("missing --pid limitation:\n%s", s)
	}

	errb.Reset()
	if code := runNetworkTopWithIO(context.Background(), []string{"--pid", "999"}, &out, &errb, deps); code != 1 || !strings.Contains(errb.String(), "999") {
		t.Errorf("unknown pid: code %d stderr %q", code, errb.String())
	}
}

func TestRunNetworkTopToolMissing(t *testing.T) {
	for kind, tool := range map[hostos.Kind]string{hostos.Darwin: "nettop", hostos.Linux: "ss"} {
		deps, f := topFakeDeps(kind, "")
		f.err = &exec.Error{Name: tool, Err: exec.ErrNotFound}
		var out, errb bytes.Buffer
		if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "5s"}, &out, &errb, deps); code != 1 {
			t.Fatalf("%s: code = %d, want 1", tool, code)
		}
		if !strings.Contains(errb.String(), tool) || !strings.Contains(errb.String(), "not found") || f.calls != 0 {
			t.Errorf("%s: stderr = %q, want the tool named", tool, errb.String())
		}
	}
}

func TestRunNetworkTopToolFailsEverySample(t *testing.T) {
	deps, f := topFakeDeps(hostos.Linux, "")
	f.err = errors.New("ss: exit status 1")
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "2s"}, &out, &errb, deps); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "no usable sample from ss in 3 attempts") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// An idle or unprivileged host lists no sockets: that is an observation,
// not a failure.
func TestRunNetworkTopIdleHostExitsZero(t *testing.T) {
	for kind, idle := range map[hostos.Kind]string{hostos.Linux: "", hostos.Darwin: ",bytes_in,bytes_out,\n,bytes_in,bytes_out,\n"} {
		deps, f := topFakeDeps(kind, idle)
		var out, errb bytes.Buffer
		if code := runNetworkTopWithIO(context.Background(), []string{"--duration", "3s"}, &out, &errb, deps); code != 0 {
			t.Fatalf("%v: code = %d stderr = %q, want 0", kind, code, errb.String())
		}
		if !strings.Contains(out.String(), "no process moved any bytes in the window") || strings.Contains(out.String(), "failed") {
			t.Errorf("%v: output:\n%s", kind, out.String())
		}
		if f.calls < 3 {
			t.Errorf("%v: calls = %d", kind, f.calls)
		}
	}
}

func TestRunNetworkTopCtrlCMac(t *testing.T) {
	deps, f := topFakeDeps(hostos.Darwin, topRuns...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.during = func(call int) error {
		if call == 1 {
			f.clk.Advance(500 * time.Millisecond)
			cancel()
			return ctx.Err()
		}
		return nil
	}
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(ctx, []string{"--duration", "10s"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "Network top over 1.0s (2 samples") || !strings.Contains(s, "Google Chrome Helper") {
		t.Errorf("summary should cover the completed interval only:\n%s", s)
	}
}

func TestRunNetworkTopCtrlC(t *testing.T) {
	ss := func(in int64) string {
		return fmt.Sprintf("ESTAB 0 0 10.0.0.5:51000 140.82.112.3:443 users:((\"git\",pid=200,fd=5))\n\t cubic bytes_acked:0 bytes_received:%d\n", in)
	}
	deps, f := topFakeDeps(hostos.Linux, ss(0), ss(100), ss(200))
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

func TestRunNetworkTopRoundsIntervalOnMac(t *testing.T) {
	deps, f := topFakeDeps(hostos.Darwin, topRuns...)
	var out, errb bytes.Buffer
	if code := runNetworkTopWithIO(context.Background(), []string{"--interval", "500ms", "--duration", "1500ms"}, &out, &errb, deps); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if s := out.String(); !strings.Contains(s, "Sampling nettop every 1s for 2s") || !strings.Contains(s, "rounded up from 500ms") {
		t.Errorf("output:\n%s", s)
	}
	if strings.Join(f.secs, ",") != "1,1" {
		t.Errorf("nettop -s = %v", f.secs)
	}
}

func TestRunNetworkTopWriteError(t *testing.T) {
	deps, _ := topFakeDeps(hostos.Darwin, topRuns...)
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
	deps, _ := topFakeDeps(hostos.Darwin, topRuns...)
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
	deps, _ := topFakeDeps(hostos.Unknown, topRuns...)
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
