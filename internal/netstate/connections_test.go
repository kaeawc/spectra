package netstate

import (
	"errors"
	"testing"
)

// lsof -i -P -n output fixture (header + representative rows).
const lsofFixture = `COMMAND    PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
Slack      412   alice  29u  IPv4 0xabc          0t0  TCP  192.168.1.100:55123->52.1.2.3:443 (ESTABLISHED)
Slack      412   alice  31u  IPv4 0xabc          0t0  TCP  *:3000 (LISTEN)
firefox    789   alice  41u  IPv4 0xdef          0t0  UDP  192.168.1.100:54812->8.8.8.8:53
netbiosd   999   root   6u   IPv4 0x111          0t0  UDP  *:138
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

func TestCollectConnectionsError(t *testing.T) {
	run := func(string, ...string) ([]byte, error) {
		return nil, errors.New("lsof failed")
	}
	if conns := CollectConnections(run); len(conns) != 0 {
		t.Errorf("expected nil on lsof error, got %d conns", len(conns))
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
