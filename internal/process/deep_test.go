package process

import (
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
	_ = CollectAll(nil, opts)
	if !deepCalled {
		t.Error("expected lsof to be called when Deep=true")
	}
}
