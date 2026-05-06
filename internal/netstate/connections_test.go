package netstate

import (
	"errors"
	"testing"

	"github.com/kaeawc/spectra/internal/process"
)

// lsof -i -P -n output fixture (header + representative rows).
const lsofFixture = `COMMAND    PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
Slack      412   alice  29u  IPv4 0xabc          0t0  TCP  192.168.1.100:55123->52.1.2.3:443 (ESTABLISHED)
Slack      412   alice  31u  IPv4 0xabc          0t0  TCP  *:3000 (LISTEN)
firefox    789   alice  41u  IPv4 0xdef          0t0  UDP  192.168.1.100:54812->8.8.8.8:53
netbiosd   999   root   6u   IPv4 0x111          0t0  UDP  *:138
`

const netstatFixture = `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  192.168.1.100.55123   52.1.2.3.443          ESTABLISHED
tcp4       0      0  *.3000                *.*                   LISTEN
udp4       0      0  192.168.1.100.54812   8.8.8.8.53
udp4       0      0  *.138                 *.*
`

func TestParseLSOFConnectionsCount(t *testing.T) {
	conns := parseLSOFConnections(lsofFixture)
	// Expect 3: the LISTEN entry is excluded.
	if len(conns) != 3 {
		t.Errorf("got %d connections, want 3; conns: %+v", len(conns), conns)
	}
}

func TestParseLSOFConnectionsTCP(t *testing.T) {
	conns := parseLSOFConnections(lsofFixture)
	slack := conns[0]
	if slack.Command != "Slack" {
		t.Errorf("Command = %q, want Slack", slack.Command)
	}
	if slack.PID != 412 {
		t.Errorf("PID = %d, want 412", slack.PID)
	}
	if slack.Proto != "tcp" {
		t.Errorf("Proto = %q, want tcp", slack.Proto)
	}
	if slack.LocalAddr != "192.168.1.100:55123" {
		t.Errorf("LocalAddr = %q, want 192.168.1.100:55123", slack.LocalAddr)
	}
	if slack.RemoteAddr != "52.1.2.3:443" {
		t.Errorf("RemoteAddr = %q, want 52.1.2.3:443", slack.RemoteAddr)
	}
	if slack.State != "established" {
		t.Errorf("State = %q, want established", slack.State)
	}
}

func TestParseLSOFConnectionsUDP(t *testing.T) {
	conns := parseLSOFConnections(lsofFixture)
	ff := conns[1]
	if ff.Proto != "udp" {
		t.Errorf("Proto = %q, want udp", ff.Proto)
	}
	if ff.RemoteAddr != "8.8.8.8:53" {
		t.Errorf("RemoteAddr = %q, want 8.8.8.8:53", ff.RemoteAddr)
	}
	if ff.State != "" {
		t.Errorf("State = %q, want empty for UDP", ff.State)
	}
}

func TestParseLSOFConnectionsListenExcluded(t *testing.T) {
	for _, c := range parseLSOFConnections(lsofFixture) {
		if c.State == "listen" {
			t.Errorf("LISTEN entry leaked into connections: %+v", c)
		}
	}
}

func TestParseLSOFConnectionsEmpty(t *testing.T) {
	if conns := parseLSOFConnections(""); len(conns) != 0 {
		t.Errorf("expected empty for blank input, got %d", len(conns))
	}
}

func TestParseNetstatConnections(t *testing.T) {
	conns := parseNetstatConnections(netstatFixture)
	if len(conns) != 3 {
		t.Fatalf("got %d connections, want 3; conns: %+v", len(conns), conns)
	}
	if conns[0].PID != 0 || conns[0].Command != "" {
		t.Fatalf("netstat connection has attribution: %+v", conns[0])
	}
	if conns[0].Proto != "tcp" || conns[0].LocalAddr != "192.168.1.100:55123" || conns[0].RemoteAddr != "52.1.2.3:443" || conns[0].State != "established" {
		t.Fatalf("tcp connection = %+v", conns[0])
	}
	if conns[1].Proto != "udp" || conns[1].RemoteAddr != "8.8.8.8:53" {
		t.Fatalf("udp connection = %+v", conns[1])
	}
}

func TestCollectConnectionsError(t *testing.T) {
	run := func(string, ...string) ([]byte, error) {
		return nil, errors.New("lsof failed")
	}
	if conns := CollectConnections(run); len(conns) != 0 {
		t.Errorf("expected nil on lsof error, got %d conns", len(conns))
	}
}

func TestCollectConnectionsFallsBackToNetstat(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "lsof":
			return nil, errors.New("lsof failed")
		case "netstat":
			return []byte(netstatFixture), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
	conns := CollectConnections(run)
	if len(conns) != 3 {
		t.Errorf("CollectConnections fallback: got %d, want 3", len(conns))
	}
}

func TestCollectConnectionsSuccess(t *testing.T) {
	run := func(string, ...string) ([]byte, error) {
		return []byte(lsofFixture), nil
	}
	conns := CollectConnections(run)
	if len(conns) != 3 {
		t.Errorf("CollectConnections: got %d, want 3", len(conns))
	}
}

func TestGroupConnectionsByApp(t *testing.T) {
	conns := []Connection{
		{PID: 412, Command: "Slack", Proto: "tcp", RemoteAddr: "52.1.2.3:443"},
		{PID: 789, Command: "firefox", Proto: "udp", RemoteAddr: "8.8.8.8:53"},
		{PID: 999, Command: "netbiosd", Proto: "udp", LocalAddr: "*:138"},
	}
	procs := []process.Info{
		{PID: 412, AppPath: "/Applications/Slack.app"},
		{PID: 789, AppPath: "/Applications/Firefox.app"},
	}

	grouped := GroupConnectionsByApp(conns, procs)
	if len(grouped["/Applications/Slack.app"]) != 1 {
		t.Fatalf("Slack group = %+v, want one connection", grouped["/Applications/Slack.app"])
	}
	if grouped["/Applications/Slack.app"][0].AppPath != "/Applications/Slack.app" {
		t.Fatalf("Slack AppPath = %q", grouped["/Applications/Slack.app"][0].AppPath)
	}
	if len(grouped["/Applications/Firefox.app"]) != 1 {
		t.Fatalf("Firefox group = %+v, want one connection", grouped["/Applications/Firefox.app"])
	}
	if len(grouped[""]) != 1 {
		t.Fatalf("unattributed group = %+v, want one connection", grouped[""])
	}
	if grouped[""][0].AppPath != "" {
		t.Fatalf("unattributed AppPath = %q, want empty", grouped[""][0].AppPath)
	}
}

func TestGroupConnectionsByAppEmptyInputs(t *testing.T) {
	grouped := GroupConnectionsByApp(nil, nil)
	if len(grouped) != 0 {
		t.Fatalf("GroupConnectionsByApp(nil, nil) = %+v, want empty map", grouped)
	}
}
