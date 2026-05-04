package process

import (
	"context"
	"errors"
	"testing"
)

// fakePS returns a CmdRunner that feeds canned ps output.
func fakePS(output string) func(string, ...string) ([]byte, error) {
	return func(name string, args ...string) ([]byte, error) {
		return []byte(output), nil
	}
}

// Synthetic ps output: pid ppid rss vsz uid user command...
// Matches the format from `ps -axwwo pid=,ppid=,rss=,vsz=,uid=,user=,command=`
const psFixture = `1      0    4096    8192 0 root /sbin/launchd
412   1    184320  409600 501 alice /Applications/Slack.app/Contents/MacOS/Slack --some-flag
415   412  92160   204800 501 alice /Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper --type=renderer
999   1    2048    4096 501 alice /bin/bash -l
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
	procs := parsePS("412 1 100")
	if len(procs) != 0 {
		t.Errorf("expected empty for short row, got %d", len(procs))
	}
}

func TestCollectAll(t *testing.T) {
	opts := CollectOptions{CmdRunner: fakePS(psFixture)}
	procs := CollectAll(context.Background(), opts)
	if len(procs) != 4 {
		t.Errorf("CollectAll: got %d procs, want 4", len(procs))
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
