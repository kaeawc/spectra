package hostload

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

const (
	runnerDir = "/Users/tester/actions-runner"
	ciRepo    = runnerDir + "/_work/app/app"
)

// runnerTable is a self-hosted GitHub Actions runner running a Swift build,
// plus one agent lane under /wt using laneCPU.
func runnerTable(laneCPU float64) ([]process.Info, map[int]string) {
	procs := []process.Info{
		{PID: 100, PPID: 1, Command: "Runner.Listener", CPUPct: 0.5},
		{PID: 101, PPID: 100, Command: "/Users/tester/actions-runner/bin/Runner.Worker", CPUPct: 1},
		{PID: 102, PPID: 101, Command: "bash", CPUPct: 0},
		{PID: 103, PPID: 102, Command: "swift-frontend", CPUPct: 180},
		{PID: 104, PPID: 102, Command: "swift-frontend", CPUPct: 120},
		{PID: 200, PPID: 1, Command: "zsh"},
		{PID: 201, PPID: 200, Command: "go", CPUPct: laneCPU},
	}
	cwds := map[int]string{
		100: runnerDir, 101: runnerDir,
		102: ciRepo, 103: ciRepo + "/Sources", 104: ciRepo,
		200: "/wt/lane1", 201: "/wt/lane1",
	}
	return procs, cwds
}

func ciFS() FS {
	return FS{Exists: func(p string) bool { return p == ciRepo+"/.git" || p == "/wt/lane1/.git" }}
}

func TestRunnerWorkLeadsAsCIRunner(t *testing.T) {
	procs, cwds := runnerTable(50)
	a := Analyze(procs, cwds, Options{TopN: 5, Roots: []Root{{Path: "/wt"}}, FS: ciFS(), Now: time.Unix(1e9, 0)})
	c := a.Cause
	if c.Kind != CauseCIRunner || c.Runner != RunnerGitHubActions || c.Repo != ciRepo {
		t.Fatalf("cause = %+v", c)
	}
	if c.CPUPct != 301.5 {
		t.Fatalf("ci cpu = %v, want 301.5", c.CPUPct)
	}
	if !contains(c.Detail, ciRepo) || !contains(c.Detail, "swift-frontend (pid 103)") {
		t.Fatalf("detail = %q", c.Detail)
	}
	if a.Families[0].Kind != FamilyCIRunner || a.Families[0].Members != 5 || a.Families[0].Name != "GitHub Actions runner" {
		t.Fatalf("families = %+v", a.Families)
	}
}

func TestLaneUnderRootStillWinsOverRunner(t *testing.T) {
	procs, cwds := runnerTable(400)
	a := Analyze(procs, cwds, Options{Roots: []Root{{Path: "/wt"}}, FS: ciFS()})
	if a.Cause.Kind != CauseRoots || a.Cause.Worktree != "lane1" {
		t.Fatalf("cause = %+v", a.Cause)
	}
	// A tie also goes to the lane.
	procs, cwds = runnerTable(301.5)
	if c := Analyze(procs, cwds, Options{Roots: []Root{{Path: "/wt"}}, FS: ciFS()}).Cause; c.Kind != CauseRoots {
		t.Fatalf("tie cause = %+v", c)
	}
}

func TestRunnerChildUnderRootCountsForTheRoot(t *testing.T) {
	// A job step that cds into a --root is that root's work, not the runner's.
	procs := []process.Info{
		{PID: 1, Command: "Runner.Worker", CPUPct: 1},
		{PID: 2, PPID: 1, Command: "go", CPUPct: 200},
	}
	cwds := map[int]string{1: runnerDir, 2: "/wt/lane2"}
	a := Analyze(procs, cwds, Options{Roots: []Root{{Path: "/wt"}}})
	if a.Cause.Kind != CauseRoots {
		t.Fatalf("cause = %+v", a.Cause)
	}
}

func TestOtherRunnersByAncestry(t *testing.T) {
	for _, tc := range []struct{ bin, want string }{
		{"gitlab-runner", RunnerGitLab},
		{"buildkite-agent", RunnerBuildkite},
	} {
		procs := []process.Info{
			{PID: 1, Command: tc.bin},
			{PID: 2, PPID: 1, Command: "make", CPUPct: 90},
		}
		a := Analyze(procs, nil, Options{})
		if a.Cause.Kind != CauseCIRunner || a.Cause.Runner != tc.want || a.Cause.Repo != "" {
			t.Fatalf("%s cause = %+v", tc.bin, a.Cause)
		}
		if !contains(a.Cause.Detail, "on this host") {
			t.Fatalf("detail = %q", a.Cause.Detail)
		}
	}
}

func TestCwdUnderWorkWithoutRunnerAncestor(t *testing.T) {
	// The runner itself may be missing from the table (another user).
	procs := []process.Info{{PID: 7, PPID: 1, Command: "xcodebuild", CPUPct: 60}}
	a := Analyze(procs, map[int]string{7: "/opt/actions-runner-2/_work/svc/svc/ios"}, Options{})
	if a.Cause.Kind != CauseCIRunner || a.Cause.Repo != "/opt/actions-runner-2/_work/svc/svc" {
		t.Fatalf("cause = %+v", a.Cause)
	}
}

func TestRunnerInstallsSumAcrossJobDirectories(t *testing.T) {
	// Two runner installs each build less than the lane outside any root,
	// but together they lead.
	procs := []process.Info{
		{PID: 1, Command: "Runner.Worker"},
		{PID: 2, PPID: 1, Command: "swift-frontend", CPUPct: 90},
		{PID: 3, Command: "Runner.Worker"},
		{PID: 4, PPID: 3, Command: "swift-frontend", CPUPct: 80},
		{PID: 5, Command: "git", CPUPct: 120},
	}
	cwds := map[int]string{
		2: "/Users/tester/actions-runner-2/_work/app/app",
		4: "/Users/tester/actions-runner-4/_work/app/app",
		5: "/Users/tester/src/app",
	}
	c := Analyze(procs, cwds, Options{}).Cause
	if c.Kind != CauseCIRunner || c.CPUPct != 170 || c.Repo != "/Users/tester/actions-runner-2/_work/app/app" {
		t.Fatalf("cause = %+v", c)
	}
	if !contains(c.Detail, "across 2 job directories") {
		t.Fatalf("detail = %q", c.Detail)
	}
}

func TestGitHubWorkRepo(t *testing.T) {
	tests := []struct {
		cwd, repo string
		ok        bool
	}{
		{"/Users/tester/actions-runner/_work/app/app/src", "/Users/tester/actions-runner/_work/app/app", true},
		{"/Users/tester/actions-runner/_work/app", "/Users/tester/actions-runner/_work/app", true},
		{"/Users/tester/actions-runner/_work/app/other", "/Users/tester/actions-runner/_work/app", true},
		{"/Users/tester/actions-runner/_work/_temp", "", true},
		{"/Users/tester/actions-runner/_work", "", true},
		{"/Users/tester/actions-runner", "", false},
		{"/Users/tester/src/_work/app", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		repo, ok := GitHubWorkRepo(tc.cwd)
		if repo != tc.repo || ok != tc.ok {
			t.Errorf("GitHubWorkRepo(%q) = %q, %v; want %q, %v", tc.cwd, repo, ok, tc.repo, tc.ok)
		}
	}
}
