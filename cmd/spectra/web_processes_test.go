package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/process"
)

func TestClassifyChromiumRole(t *testing.T) {
	cases := []struct {
		cmdline string
		role    string
		sub     string
		client  string
	}{
		{"/Applications/Slack.app/.../Slack", "browser", "", ""},
		{"Slack Helper --type=renderer --renderer-client-id=7", "renderer", "", "7"},
		{"Slack Helper (GPU) --type=gpu-process", "gpu", "", ""},
		{"Slack Helper --type=utility --utility-sub-type=network.mojom.NetworkService", "utility", "network.mojom.NetworkService", ""},
		{"proc --type=zygote", "zygote", "", ""},
	}
	for _, c := range cases {
		got := classifyChromiumRole(c.cmdline)
		if got.Role != c.role || got.SubType != c.sub || got.ClientID != c.client {
			t.Errorf("classify(%q) = %+v, want role=%s sub=%s client=%s", c.cmdline, got, c.role, c.sub, c.client)
		}
	}
}

func rendererProc(pid int, clientID string, rssMiB int64) process.Info {
	return process.Info{
		PID:             pid,
		AppPath:         "/Applications/Slack.app",
		RSSKiB:          rssMiB * 1024,
		FullCommandLine: "Slack Helper --type=renderer --renderer-client-id=" + clientID,
	}
}

func TestBuildTopologyRolesAndTotals(t *testing.T) {
	procs := []process.Info{
		{PID: 1, AppPath: "/Applications/Slack.app", RSSKiB: 120 * 1024, FullCommandLine: "/Applications/Slack.app/Contents/MacOS/Slack"},
		{PID: 2, AppPath: "/Applications/Slack.app", RSSKiB: 380 * 1024, FullCommandLine: "Slack Helper (GPU) --type=gpu-process"},
		rendererProc(3, "1", 200),
		rendererProc(4, "2", 210),
		rendererProc(5, "3", 190),
	}
	topo := buildTopology("Slack", procs)
	if topo.Processes != 5 {
		t.Errorf("processes = %d, want 5", topo.Processes)
	}
	if topo.TotalRSSKiB != (120+380+200+210+190)*1024 {
		t.Errorf("total = %d", topo.TotalRSSKiB)
	}
	// roles sorted by RSS desc; renderer group (600MB) should be first
	if topo.Roles[0].Role != "renderer" || topo.Roles[0].Count != 3 {
		t.Errorf("top role = %+v, want renderer x3", topo.Roles[0])
	}
	if topo.OutlierRenderer != nil {
		t.Errorf("balanced renderers should have no outlier, got %+v", topo.OutlierRenderer)
	}
}

func TestBuildTopologyOutlier(t *testing.T) {
	procs := []process.Info{
		rendererProc(3, "1", 200),
		rendererProc(4, "2", 210),
		rendererProc(5, "7", 1200), // ~6x median
		rendererProc(6, "3", 190),
		rendererProc(7, "4", 205),
	}
	topo := buildTopology("Slack", procs)
	if topo.OutlierRenderer == nil {
		t.Fatal("expected an outlier renderer")
	}
	if topo.OutlierRenderer.PID != 5 || topo.OutlierRenderer.ClientID != "7" {
		t.Errorf("outlier = %+v, want pid 5 client 7", topo.OutlierRenderer)
	}
	if topo.OutlierRenderer.MedianMultiple < 2.5 {
		t.Errorf("median multiple = %v, want >= 2.5", topo.OutlierRenderer.MedianMultiple)
	}
}

func TestRunWebProcessesRenders(t *testing.T) {
	collect := func(string) []process.Info {
		return []process.Info{
			{PID: 1, AppPath: "/Applications/Slack.app", RSSKiB: 120 * 1024, FullCommandLine: "/Applications/Slack.app/Contents/MacOS/Slack"},
			rendererProc(3, "1", 200),
		}
	}
	var out, errBuf bytes.Buffer
	if code := runWebProcesses([]string{"/Applications/Slack.app"}, &out, &errBuf, collect); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"Slack", "browser", "renderer", "topology"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestRunWebProcessesNone(t *testing.T) {
	collect := func(string) []process.Info { return nil }
	var out, errBuf bytes.Buffer
	if code := runWebProcesses([]string{"/Applications/Slack.app"}, &out, &errBuf, collect); code != 1 {
		t.Fatalf("exit = %d, want 1 when no processes", code)
	}
}

func TestRunWebProcessesWrongArgc(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runWebProcesses([]string{}, &out, &errBuf, func(string) []process.Info { return nil }); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
