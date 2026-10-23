package xpcattr

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testProcs() []process.Info {
	return []process.Info{
		{PID: 1, Command: "launchd"},
		{PID: 17491, PPID: 1, Command: "xcodebuild", FullCommandLine: "xcodebuild test"},
		{PID: 17500, PPID: 17491, Command: "xctest-helper"},
		{PID: 17649, PPID: 17500, Command: "CtrlProxyUITests-Runner", FullCommandLine: "/x/CtrlProxyUITests-Runner"},
		{PID: 20001, PPID: 1, Command: "Terminal"},
	}
}

func TestParseQueueClientsMulti(t *testing.T) {
	got := ParseQueueClients(fixture(t, "sample_multi_client.txt"))
	if len(got) != 2 || got[17649] != 800 || got[20001] != 200 {
		t.Errorf("got %v", got)
	}
}

func TestParseQueueClientsNoPIDAndGarbage(t *testing.T) {
	if got := ParseQueueClients(fixture(t, "sample_no_pid.txt")); len(got) != 0 {
		t.Errorf("no-pid fixture: %v", got)
	}
	if got := ParseQueueClients(fixture(t, "garbage.txt")); len(got) != 0 {
		t.Errorf("garbage: %v", got)
	}
	if len(ParseQueueClients("")) != 0 {
		t.Error("empty input")
	}
}

func TestResolveChainAndLauncher(t *testing.T) {
	cs := Resolve(map[int]int{17649: 800, 20001: 200}, testProcs())
	top := cs[0]
	if top.PID != 17649 || top.Share != 0.8 || top.Name != "CtrlProxyUITests-Runner" {
		t.Fatalf("top = %+v", top)
	}
	if len(top.Chain) != 2 || top.Launcher == nil || top.Launcher.Name != "xcodebuild" || top.Launcher.PID != 17491 {
		t.Errorf("chain/launcher = %+v %+v", top.Chain, top.Launcher)
	}
	if cs[1].PID != 20001 || cs[1].Launcher != nil {
		t.Errorf("Terminal under launchd should have no launcher: %+v", cs[1])
	}
	cs = Resolve(map[int]int{999: 1}, nil)
	last := cs[0]
	if last.PID != 999 || last.Name != "" {
		t.Errorf("unknown pid = %+v", last)
	}
}

func TestResolveCycle(t *testing.T) {
	procs := []process.Info{{PID: 10, PPID: 11, Command: "a"}, {PID: 11, PPID: 10, Command: "b"}}
	cs := Resolve(map[int]int{10: 1}, procs)
	if len(cs[0].Chain) != 1 {
		t.Errorf("chain = %+v", cs[0].Chain)
	}
}

func TestFromSampleSummary(t *testing.T) {
	a := FromSample(17874, "OSLogService", fixture(t, "sample_multi_client.txt"), "", testProcs())
	want := "OSLogService (17874) busy, driven by CtrlProxyUITests-Runner (17649), launched by xcodebuild (17491)"
	if a.Source != SourceSample || a.Summary() != want {
		t.Errorf("source=%s summary=%q", a.Source, a.Summary())
	}
}

func TestFromSampleLsmpFallback(t *testing.T) {
	a := FromSample(17874, "OSLogService", fixture(t, "sample_no_pid.txt"), fixture(t, "lsmp_holders.txt"), testProcs())
	if a.Source != SourceLsmp || len(a.Clients) != 2 || a.Clients[0].PID != 17649 || a.Clients[0].Weight != 2 {
		t.Errorf("attribution = %+v", a)
	}
}

func TestSummaryEmpty(t *testing.T) {
	if s := FromSample(1, "x", "nothing", "", nil).Summary(); s != "" {
		t.Errorf("summary = %q", s)
	}
}

func TestCaptureUnsupportedOffDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	if _, err := Capture(context.Background(), Options{PID: 1}); err == nil {
		t.Fatal("expected unsupported error")
	}
}

func TestCaptureDarwinFallsBackToLsmp(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	var cmds []string
	fake := runnerFunc(func(c proc.Cmd) (proc.Result, error) {
		cmds = append(cmds, c.Name)
		if c.Name == "sample" {
			return proc.Result{Stdout: []byte(fixture(t, "sample_no_pid.txt"))}, nil
		}
		return proc.Result{Stdout: []byte(fixture(t, "lsmp_holders.txt"))}, nil
	})
	a, err := Capture(context.Background(), Options{PID: 17874, Runner: fake,
		Procs: func(context.Context) []process.Info { return testProcs() }})
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != SourceLsmp || a.Name != "OSLogService" || strings.Join(cmds, ",") != "sample,/usr/bin/lsmp" {
		t.Errorf("a=%+v cmds=%v", a, cmds)
	}
}

type runnerFunc func(proc.Cmd) (proc.Result, error)

func (f runnerFunc) Run(_ context.Context, c proc.Cmd) (proc.Result, error) { return f(c) }
