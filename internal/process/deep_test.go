package process

import (
	"context"
	"errors"
	"testing"
)

// lsof -p 412,999 fixture (header + representative rows for two PIDs).
const lsofDeepFixture = `COMMAND    PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
Slack      412   alice  cwd    DIR               1,15      512          222552 /Applications/Slack.app
Slack      412   alice  txt    REG               1,15  1234567          111111 /Applications/Slack.app/Contents/MacOS/Slack
Slack      412   alice    0r   CHR                3,2      0t0             339 /dev/null
Slack      412   alice    1w   REG               1,15        0          229784 /tmp/slack.log
Slack      412   alice    2w   REG               1,15        0          229784 /tmp/slack.log
Slack      412   alice   10u  IPv4 0xabc          0t0  TCP  *:3000 (LISTEN)
Slack      412   alice   29u  IPv4 0xabc          0t0  TCP  127.0.0.1:55123->127.0.0.1:443 (ESTABLISHED)
bash       999   alice  cwd    DIR               1,15      512          222552 /home/alice
bash       999   alice  txt    REG               1,15   987654          222222 /bin/bash
bash       999   alice    0u   CHR                3,2      0t0             339 /dev/tty
bash       999   alice    1u   CHR                3,2      0t0             339 /dev/tty
bash       999   alice    2u   CHR                3,2      0t0             339 /dev/tty
`

func TestParseLSOFDeepFDCount(t *testing.T) {
	procs := []Info{
		{PID: 412, Command: "Slack"},
		{PID: 999, Command: "bash"},
	}
	parseLSOFDeep(procs, lsofDeepFixture)

	// Slack: FDs 0, 1, 2, 10, 29 = 5 open FDs (digit-prefixed rows).
	if procs[0].OpenFDs != 5 {
		t.Errorf("Slack OpenFDs = %d, want 5", procs[0].OpenFDs)
	}
	// bash: FDs 0, 1, 2 = 3 open FDs.
	if procs[1].OpenFDs != 3 {
		t.Errorf("bash OpenFDs = %d, want 3", procs[1].OpenFDs)
	}
}

func TestParseLSOFDeepFDBreakdown(t *testing.T) {
	const fixture = `COMMAND    PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
App        777   alice    0u   CHR                3,2      0t0     339 /dev/ptmx
App        777   alice    1u   CHR                3,2      0t0     339 /dev/ttys000
App        777   alice    2u   CHR                3,2      0t0     339 /dev/ptyq1
App        777   alice    3u  IPv4 0xabc          0t0     TCP 127.0.0.1:1->127.0.0.1:2 (ESTABLISHED)
App        777   alice    4u  IPv6 0xabc          0t0     TCP [::1]:1->[::1]:2 (ESTABLISHED)
App        777   alice    5u  unix 0xabc          0t0         0 /var/run/app.sock
App        777   alice    6u  IPv4 0xabc          0t0     TCP *:3000 (LISTEN)
App        777   alice    7u  unix 0xabc          0t0         0 /var/run/app2.sock
App        777   alice    8r   REG               1,15        0     100 /tmp/a
App        777   alice    9r   REG               1,15        0     101 /tmp/b
App        777   alice   10r   REG               1,15        0     102 /tmp/c
App        777   alice   11r   REG               1,15        0     103 /tmp/d
App        777   alice   12r   REG               1,15        0     104 /tmp/e
App        777   alice   13r   REG               1,15        0     105 /tmp/f
App        777   alice   14r   REG               1,15        0     106 /tmp/g
App        777   alice   15r   REG               1,15        0     107 /tmp/h
App        777   alice   16r   REG               1,15        0     108 /tmp/i
App        777   alice   17r   REG               1,15        0     109 /tmp/j
App        777   alice   18u  PIPE               0,15      0t0         0
App        777   alice   19u  FIFO               0,15      0t0         0
App        777   alice   20u KQUEUE                                        count=1, state=0
`
	procs := []Info{{PID: 777, Command: "App"}}
	parseLSOFDeep(procs, fixture)

	b := procs[0].FDBreakdown
	if b == nil {
		t.Fatal("FDBreakdown is nil")
	}
	if procs[0].OpenFDs != 21 {
		t.Fatalf("OpenFDs = %d, want 21", procs[0].OpenFDs)
	}
	if b.Total != procs[0].OpenFDs {
		t.Fatalf("breakdown total = %d, want OpenFDs %d", b.Total, procs[0].OpenFDs)
	}
	if b.PTY != 3 || b.Socket != 5 || b.Regular != 10 || b.Pipe != 2 || b.Kqueue != 1 {
		t.Fatalf("breakdown = %+v, want pty=3 socket=5 regular=10 pipe=2 kqueue=1", *b)
	}
}

func TestParseLSOFDeepFDBreakdownCharDevice(t *testing.T) {
	const fixture = `COMMAND    PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
App        777   alice    0u   CHR                3,2      0t0     339 /dev/null
`
	procs := []Info{{PID: 777, Command: "App"}}
	parseLSOFDeep(procs, fixture)

	b := procs[0].FDBreakdown
	if b == nil {
		t.Fatal("FDBreakdown is nil")
	}
	if b.Char != 1 || b.PTY != 0 {
		t.Fatalf("breakdown = %+v, want char=1 pty=0", *b)
	}
}

func TestParseLSOFDeepFDBreakdownAddsUp(t *testing.T) {
	procs := []Info{
		{PID: 412, Command: "Slack"},
		{PID: 999, Command: "bash"},
	}
	parseLSOFDeep(procs, lsofDeepFixture)

	for _, p := range procs {
		if p.FDBreakdown == nil {
			t.Fatalf("%s FDBreakdown is nil", p.Command)
		}
		sum := p.FDBreakdown.PTY + p.FDBreakdown.Socket + p.FDBreakdown.Regular +
			p.FDBreakdown.Dir + p.FDBreakdown.Pipe + p.FDBreakdown.Char +
			p.FDBreakdown.Kqueue + p.FDBreakdown.Other
		if sum != p.OpenFDs || p.FDBreakdown.Total != p.OpenFDs {
			t.Fatalf("%s breakdown = %+v, OpenFDs = %d, category sum = %d", p.Command, *p.FDBreakdown, p.OpenFDs, sum)
		}
	}
}

func TestParseLSOFDeepListeningPorts(t *testing.T) {
	procs := []Info{
		{PID: 412, Command: "Slack"},
		{PID: 999, Command: "bash"},
	}
	parseLSOFDeep(procs, lsofDeepFixture)

	if len(procs[0].ListeningPorts) != 1 || procs[0].ListeningPorts[0] != 3000 {
		t.Errorf("Slack ListeningPorts = %v, want [3000]", procs[0].ListeningPorts)
	}
	if len(procs[1].ListeningPorts) != 0 {
		t.Errorf("bash ListeningPorts = %v, want []", procs[1].ListeningPorts)
	}
}

func TestParseLSOFDeepOutboundConnections(t *testing.T) {
	procs := []Info{
		{PID: 412, Command: "Slack"},
		{PID: 999, Command: "bash"},
	}
	parseLSOFDeep(procs, lsofDeepFixture)

	if len(procs[0].OutboundConnections) != 1 || procs[0].OutboundConnections[0] != "127.0.0.1:443" {
		t.Errorf("Slack OutboundConnections = %v, want [127.0.0.1:443]", procs[0].OutboundConnections)
	}
	if len(procs[1].OutboundConnections) != 0 {
		t.Errorf("bash OutboundConnections = %v, want []", procs[1].OutboundConnections)
	}
}

func TestParseLSOFDeepLogFiles(t *testing.T) {
	// fixture has Slack writing to /tmp/slack.log on FDs 1w and 2w (duplicates).
	// bash has only /dev/tty (CHR, not REG, not log-shaped).
	procs := []Info{
		{PID: 412, Command: "Slack"},
		{PID: 999, Command: "bash"},
	}
	parseLSOFDeep(procs, lsofDeepFixture)

	if len(procs[0].LogFiles) != 1 || procs[0].LogFiles[0] != "/tmp/slack.log" {
		t.Errorf("Slack LogFiles = %v, want [/tmp/slack.log]", procs[0].LogFiles)
	}
	if len(procs[1].LogFiles) != 0 {
		t.Errorf("bash LogFiles = %v, want []", procs[1].LogFiles)
	}
}

func TestIsLogShapedPath(t *testing.T) {
	cases := map[string]bool{
		"/tmp/slack.log":                                      true,
		"/Users/foo/Library/Logs/Slack/main.log":              true,
		"/Users/foo/Library/Application Support/Slack/Logs/x": true,
		"/var/log/system.log":                                 true,
		"/Users/foo/Documents/Catalogs/index":                 false, // "Catalogs/", not "Logs/"
		"/Users/foo/work/Dialogs/notes":                       false, // "Dialogs/", not "Logs/"
		"/Applications/Slack.app/Contents/MacOS/Slack":        false,
		"": false,
	}
	for in, want := range cases {
		if got := isLogShapedPath(in); got != want {
			t.Errorf("isLogShapedPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFdIsWritable(t *testing.T) {
	cases := map[string]bool{
		"1w":  true,
		"12u": true,
		"9r":  false,
		"":    false,
		"cwd": false,
		"txt": false,
	}
	for in, want := range cases {
		if got := fdIsWritable(in); got != want {
			t.Errorf("fdIsWritable(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseLSOFDeepUnknownPIDSkipped(t *testing.T) {
	// PID 999 not in procs — should not panic or produce errors.
	procs := []Info{{PID: 412, Command: "Slack"}}
	parseLSOFDeep(procs, lsofDeepFixture)
	if procs[0].OpenFDs == 0 {
		t.Error("expected non-zero OpenFDs for Slack")
	}
}

func TestEnrichDeepError(t *testing.T) {
	procs := []Info{{PID: 412, Command: "Slack"}}
	run := func(string, ...string) ([]byte, error) {
		return nil, nil // empty output, no error
	}
	// Should not panic; FDs stay at 0 with empty output.
	enrichDeep(procs, run)
	if procs[0].OpenFDs != 0 {
		t.Errorf("expected OpenFDs=0 for empty lsof output, got %d", procs[0].OpenFDs)
	}
}

func TestEnrichDeepParsesPartialOutputOnError(t *testing.T) {
	procs := []Info{{PID: 412, Command: "Slack"}}
	run := func(string, ...string) ([]byte, error) {
		return []byte(lsofDeepFixture), errors.New("lsof: partial failure")
	}

	enrichDeep(procs, run)
	if procs[0].OpenFDs != 5 {
		t.Fatalf("OpenFDs = %d, want 5", procs[0].OpenFDs)
	}
	if procs[0].FDBreakdown == nil || procs[0].FDBreakdown.Total != 5 {
		t.Fatalf("FDBreakdown = %+v, want total 5", procs[0].FDBreakdown)
	}
}

func TestCollectAllDeep(t *testing.T) {
	deepCalled := false
	lsofOut := `COMMAND  PID  USER  FD  TYPE  DEVICE  SIZE  NODE  NAME
Slack    412  alice  0r  CHR   3,2   0t0   339   /dev/null
Slack    412  alice  1w  REG   1,15  0     123   /tmp/out
`
	opts := CollectOptions{
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			if name == "lsof" {
				deepCalled = true
				return []byte(lsofOut), nil
			}
			return []byte(psFixture), nil
		},
		Deep: true,
	}
	_ = CollectAll(context.TODO(), opts)
	if !deepCalled {
		t.Error("expected lsof to be called when Deep=true")
	}
}
