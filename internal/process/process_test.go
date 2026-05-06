package process

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePS returns a CmdRunner that feeds canned ps output.
func fakePS(output string) func(string, ...string) ([]byte, error) {
	return func(name string, args ...string) ([]byte, error) {
		return []byte(output), nil
	}
}

// psFixture matches the ps format: pid ppid pcpu rss vsz uid user lstart(5) command
// lstart is "Dow Mon DD HH:MM:SS YYYY" — always 5 tokens after Fields().
const psFixture = `1      0   0.0   4096    8192 0 root Sat May  2 22:37:01 2026 /sbin/launchd
412   1    1.2  184320  409600 501 alice Sat May  2 22:40:00 2026 /Applications/Slack.app/Contents/MacOS/Slack --some-flag
415   412  0.5  92160   204800 501 alice Sat May  2 22:40:05 2026 /Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper --type=renderer
999   1    0.0  2048    4096 501 alice Sat May  2 23:00:00 2026 /bin/bash -l
`

func TestParsePS(t *testing.T) {
	procs := parsePS(psFixture)
	if len(procs) != 4 {
		t.Fatalf("got %d procs, want 4; procs: %+v", len(procs), procs)
	}
}

func TestParsePSFields(t *testing.T) {
	procs := parsePS(psFixture)
	slack := procs[1]
	if slack.PID != 412 {
		t.Errorf("PID = %d, want 412", slack.PID)
	}
	if slack.PPID != 1 {
		t.Errorf("PPID = %d, want 1", slack.PPID)
	}
	if slack.ThreadCount != 0 {
		t.Errorf("ThreadCount = %d, want 0 (nlwp not available on macOS)", slack.ThreadCount)
	}
	if slack.CPUPct != 1.2 {
		t.Errorf("CPUPct = %v, want 1.2", slack.CPUPct)
	}
	if slack.RSSKiB != 184320 {
		t.Errorf("RSSKiB = %d, want 184320", slack.RSSKiB)
	}
	if slack.VSizeKiB != 409600 {
		t.Errorf("VSizeKiB = %d, want 409600", slack.VSizeKiB)
	}
	if slack.User != "alice" {
		t.Errorf("User = %q, want alice", slack.User)
	}
	if slack.Command != "Slack" {
		t.Errorf("Command = %q, want Slack (short name)", slack.Command)
	}
	if slack.FullCommandLine != "/Applications/Slack.app/Contents/MacOS/Slack --some-flag" {
		t.Errorf("FullCommandLine = %q", slack.FullCommandLine)
	}
}

func TestParsePSEmptyLines(t *testing.T) {
	procs := parsePS("\n\n")
	if len(procs) != 0 {
		t.Errorf("got %d procs for blank input", len(procs))
	}
}

func TestParsePSShortRow(t *testing.T) {
	// Fewer than 13 fields (7 fixed + 5 lstart + 1 command) → skipped.
	procs := parsePS("412 1 1 0.0 100")
	if len(procs) != 0 {
		t.Errorf("expected empty for short row, got %d", len(procs))
	}
}

func TestParsePSStartTime(t *testing.T) {
	procs := parsePS(psFixture)
	slack := procs[1]
	if slack.StartTime.IsZero() {
		t.Error("StartTime should not be zero for Slack process")
	}
	if slack.StartTime.Month().String() != "May" {
		t.Errorf("StartTime month = %v, want May", slack.StartTime.Month())
	}
	if slack.StartTime.Year() != 2026 {
		t.Errorf("StartTime year = %d, want 2026", slack.StartTime.Year())
	}
	if slack.StartTime.Location() != time.Local {
		t.Errorf("StartTime location = %v, want time.Local", slack.StartTime.Location())
	}
}

func TestCollectAll(t *testing.T) {
	opts := CollectOptions{CmdRunner: fakePS(psFixture)}
	procs := CollectAll(context.Background(), opts)
	if len(procs) != 4 {
		t.Errorf("CollectAll: got %d procs, want 4", len(procs))
	}
}

func TestCollectAllAppliesThreadCounts(t *testing.T) {
	opts := CollectOptions{
		CmdRunner: fakePS(psFixture),
		ThreadCounter: func(procs []Info) map[int]int {
			if len(procs) != 4 {
				t.Fatalf("thread counter saw %d procs, want 4", len(procs))
			}
			return map[int]int{
				412: 13,
				415: 7,
			}
		},
	}
	procs := CollectAll(context.Background(), opts)
	if procs[1].ThreadCount != 13 {
		t.Errorf("Slack ThreadCount = %d, want 13", procs[1].ThreadCount)
	}
	if procs[2].ThreadCount != 7 {
		t.Errorf("Slack Helper ThreadCount = %d, want 7", procs[2].ThreadCount)
	}
	if procs[0].ThreadCount != 0 {
		t.Errorf("launchd ThreadCount = %d, want 0", procs[0].ThreadCount)
	}
}

func TestCollectAllAppliesProcessDetails(t *testing.T) {
	opts := CollectOptions{
		CmdRunner: fakePS(psFixture),
		DetailCollector: func(procs []Info) map[int]Details {
			if len(procs) != 4 {
				t.Fatalf("detail collector saw %d procs, want 4", len(procs))
			}
			return map[int]Details{
				412: {
					ThreadCount:    13,
					BSDName:        "Slack",
					ExecutablePath: "/Applications/Slack.app/Contents/MacOS/Slack",
				},
			}
		},
	}
	procs := CollectAll(context.Background(), opts)
	slack := procs[1]
	if slack.ThreadCount != 13 {
		t.Errorf("ThreadCount = %d, want 13", slack.ThreadCount)
	}
	if slack.BSDName != "Slack" {
		t.Errorf("BSDName = %q, want Slack", slack.BSDName)
	}
	if slack.ExecutablePath != "/Applications/Slack.app/Contents/MacOS/Slack" {
		t.Errorf("ExecutablePath = %q", slack.ExecutablePath)
	}
}

func TestCollectAllPSError(t *testing.T) {
	opts := CollectOptions{
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			return nil, errors.New("ps failed")
		},
	}
	procs := CollectAll(context.Background(), opts)
	if len(procs) != 0 {
		t.Errorf("expected nil on ps error, got %d procs", len(procs))
	}
}

func TestBundleAttributionUsesExecutablePath(t *testing.T) {
	opts := CollectOptions{
		CmdRunner:   fakePS("700 1 0.0 1024 2048 501 alice Sat May  2 23:00:00 2026 Helper --flag\n"),
		BundlePaths: []string{"/Applications/Foo Bar.app"},
		DetailCollector: func([]Info) map[int]Details {
			return map[int]Details{
				700: {ExecutablePath: "/Applications/Foo Bar.app/Contents/MacOS/Helper"},
			}
		},
	}
	procs := CollectAll(context.Background(), opts)
	if len(procs) != 1 {
		t.Fatalf("CollectAll = %d procs, want 1", len(procs))
	}
	if procs[0].AppPath != "/Applications/Foo Bar.app" {
		t.Errorf("AppPath = %q, want /Applications/Foo Bar.app", procs[0].AppPath)
	}
}

func TestBundleAttribution(t *testing.T) {
	opts := CollectOptions{
		CmdRunner:   fakePS(psFixture),
		BundlePaths: []string{"/Applications/Slack.app"},
	}
	procs := CollectAll(context.Background(), opts)

	slackCount := 0
	for _, p := range procs {
		if p.AppPath == "/Applications/Slack.app" {
			slackCount++
		}
	}
	if slackCount != 2 {
		t.Errorf("Slack attributed processes = %d, want 2 (main + helper)", slackCount)
	}

	// launchd and bash should have no AppPath
	for _, p := range procs {
		if p.Command == "launchd" || p.Command == "bash" || p.Command == "-l" {
			if p.AppPath != "" {
				t.Errorf("%s incorrectly attributed to %q", p.Command, p.AppPath)
			}
		}
	}
}

func TestGroupByApp(t *testing.T) {
	opts := CollectOptions{
		CmdRunner:   fakePS(psFixture),
		BundlePaths: []string{"/Applications/Slack.app"},
	}
	procs := CollectAll(context.Background(), opts)
	groups := GroupByApp(procs)

	slack := groups["/Applications/Slack.app"]
	if len(slack) != 2 {
		t.Errorf("Slack group = %d procs, want 2", len(slack))
	}
	noApp := groups[""]
	if len(noApp) != 2 {
		t.Errorf("unattributed group = %d procs, want 2 (launchd + bash)", len(noApp))
	}
}

func TestTotalRSS(t *testing.T) {
	procs := parsePS(psFixture)
	total := TotalRSS(procs)
	// 4096 + 184320 + 92160 + 2048 = 282624
	if total != 282624 {
		t.Errorf("TotalRSS = %d, want 282624", total)
	}
}

func TestTotalRSSEmpty(t *testing.T) {
	if TotalRSS(nil) != 0 {
		t.Error("TotalRSS(nil) should be 0")
	}
}

func TestBuildTreeEmpty(t *testing.T) {
	roots := BuildTree(nil)
	if len(roots) != 0 {
		t.Errorf("BuildTree(nil) = %d roots, want 0", len(roots))
	}
}

func TestBuildTreeSingleRoot(t *testing.T) {
	procs := []Info{{PID: 1, PPID: 0, Command: "launchd"}}
	roots := BuildTree(procs)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if roots[0].Info.PID != 1 {
		t.Errorf("root PID = %d, want 1", roots[0].Info.PID)
	}
	if len(roots[0].Children) != 0 {
		t.Errorf("unexpected children on root")
	}
}

func TestBuildTreeParentChild(t *testing.T) {
	procs := []Info{
		{PID: 1, PPID: 0, Command: "launchd"},
		{PID: 10, PPID: 1, Command: "bash"},
		{PID: 20, PPID: 10, Command: "go"},
	}
	roots := BuildTree(procs)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if len(roots[0].Children) != 1 {
		t.Fatalf("launchd children = %d, want 1", len(roots[0].Children))
	}
	bash := roots[0].Children[0]
	if bash.Info.PID != 10 {
		t.Errorf("child PID = %d, want 10", bash.Info.PID)
	}
	if len(bash.Children) != 1 || bash.Children[0].Info.PID != 20 {
		t.Errorf("bash child = %v, want PID 20", bash.Children)
	}
}

func TestBuildTreeSelfReferentialBecomesRoot(t *testing.T) {
	procs := []Info{
		{PID: 0, PPID: 0, Command: "kernel"},
		{PID: 1, PPID: 0, Command: "launchd"},
	}
	roots := BuildTree(procs)
	// PID 0 is self-referential; PID 1's parent (PID 0) is in the map
	// but PID 0 is itself a root, so PID 1 should be a child of PID 0.
	found := false
	for _, r := range roots {
		if r.Info.PID == 0 {
			found = true
			if len(r.Children) != 1 || r.Children[0].Info.PID != 1 {
				t.Errorf("kernel children = %v, want [launchd]", r.Children)
			}
		}
	}
	if !found {
		t.Error("expected PID 0 (kernel) as a root")
	}
}

func TestBuildTreeOrphanBecomesRoot(t *testing.T) {
	// Parent PID not in the list → child becomes a root.
	procs := []Info{
		{PID: 100, PPID: 999, Command: "orphan"},
	}
	roots := BuildTree(procs)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if roots[0].Info.PID != 100 {
		t.Errorf("root PID = %d, want 100", roots[0].Info.PID)
	}
}
