package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const launchctlListFixture = `PID	Status	Label
-	0	com.apple.backupd-auto
123	0	com.example.running
-	-9	com.example.exited
`

func TestParseLaunchctlList(t *testing.T) {
	jobs := ParseLaunchctlList([]byte(launchctlListFixture))
	if len(jobs) != 3 {
		t.Fatalf("jobs = %d, want 3", len(jobs))
	}
	if jobs[0].Label != "com.apple.backupd-auto" || jobs[0].PID != 0 || jobs[0].LastExitStatus != 0 {
		t.Fatalf("first job = %+v", jobs[0])
	}
	if jobs[1].PID != 123 {
		t.Fatalf("running PID = %d, want 123", jobs[1].PID)
	}
	if jobs[2].LastExitStatus != -9 {
		t.Fatalf("exit status = %d, want -9", jobs[2].LastExitStatus)
	}
}

func TestListMergesLaunchctlAndPlists(t *testing.T) {
	dir := t.TempDir()
	writePlist(t, filepath.Join(dir, "com.apple.backupd-auto.plist"), `<?xml version="1.0"?><plist version="1.0"><dict>
<key>Label</key><string>com.apple.backupd-auto</string>
<key>Program</key><string>/System/Library/CoreServices/backupd.bundle/Contents/Resources/backupd-helper</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><false/>
<key>StartInterval</key><integer>3600</integer>
<key>WatchPaths</key><array><string>/Volumes</string></array>
</dict></plist>`)
	run := RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "/bin/launchctl" && len(args) == 1 && args[0] == "list" {
			return []byte(launchctlListFixture), nil
		}
		return nil, os.ErrNotExist
	})
	inv, err := List(context.Background(), Options{
		Domain:    "system",
		Runner:    run,
		Now:       func() time.Time { return time.Unix(100, 0) },
		PlistDirs: []string{dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Jobs) != 1 {
		t.Fatalf("jobs = %d, want 1: %+v", len(inv.Jobs), inv.Jobs)
	}
	job := inv.Jobs[0]
	if job.Label != "com.apple.backupd-auto" || job.PlistPath == "" || job.PlistMTime.IsZero() {
		t.Fatalf("job not enriched from plist: %+v", job)
	}
	if !job.RunAtLoad || job.StartInterval != 3600 || len(job.WatchPaths) != 1 {
		t.Fatalf("schedule fields not parsed: %+v", job)
	}
}

func TestJobFromPlistParsesKeepAliveDict(t *testing.T) {
	job := jobFromPlist(map[string]any{
		"Label":     "com.example.agent",
		"KeepAlive": map[string]any{"SuccessfulExit": false},
		"ProgramArguments": []any{
			"/usr/bin/true",
			"--flag",
		},
	}, "/tmp/com.example.agent.plist", "user", time.Unix(1, 0))
	if !job.KeepAlive {
		t.Fatalf("KeepAlive = false, want true")
	}
	if len(job.ProgramArguments) != 2 || job.ProgramArguments[1] != "--flag" {
		t.Fatalf("ProgramArguments = %+v", job.ProgramArguments)
	}
}

func TestIsLoadedWithRunner(t *testing.T) {
	var gotName string
	var gotArgs []string
	run := RunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("service = com.apple.backupd-auto"), nil
	})
	if !IsLoadedWithRunner(context.Background(), run, "system", "com.apple.backupd-auto") {
		t.Fatal("service should be loaded")
	}
	if gotName != "/bin/launchctl" || len(gotArgs) != 2 || gotArgs[1] != "system/com.apple.backupd-auto" {
		t.Fatalf("launchctl call = %s %v", gotName, gotArgs)
	}
}

func writePlist(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
