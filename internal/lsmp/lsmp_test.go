package lsmp

import (
	"strings"
	"testing"
)

// Modeled on documented `lsmp -p` output: a header, a warning, then data rows
// keyed by a hex port name, with the rights keyword in the row.
const sampleReport = `  name      ipc-object    rights     flags   boost  reqs  recv  send  sonce  qlimit  msgcount  identifier  type
--------  ----------    ------     -----   -----  ----  ----  ----  -----  ------  --------  ----------  ----
0x00000107  0x1a2b3c4d    recv       --sN-   0      ---   1     0     0      5       0         0x0         TASK-CONTROL
0x0000020b  0x2b3c4d5e    send       -----   0      ---   0     1     0      0       0         0xdead      SEND
0x0000030c  0x3c4d5e6f    send       -----   0      ---   0     3     0      0       0         0xbeef      SEND
0x0000040d  0x4d5e6f70    send-once  -----   0      ---   0     0     1      0       0         0xfeed      SEND-ONCE
-           0x5e6f7081    port-set   -----   0      ---   0     0     0      0       0         0x0         PORT-SET
`

func TestParseCountsRights(t *testing.T) {
	s := Parse(sampleReport)
	if s.TotalPorts != 5 {
		t.Errorf("total = %d, want 5", s.TotalPorts)
	}
	if s.RecvRights != 1 {
		t.Errorf("recv = %d, want 1", s.RecvRights)
	}
	if s.SendRights != 2 {
		t.Errorf("send = %d, want 2", s.SendRights)
	}
	if s.SendOnceRights != 1 {
		t.Errorf("send-once = %d, want 1", s.SendOnceRights)
	}
	if s.PortSets != 1 {
		t.Errorf("port-sets = %d, want 1", s.PortSets)
	}
	if len(s.Notes) != 0 {
		t.Errorf("did not expect a leak note for a small table: %v", s.Notes)
	}
}

func TestParseIgnoresHeadersAndWarnings(t *testing.T) {
	report := "warning: should run as root for best output.\n" +
		"  name  ipc-object  rights\n" +
		"0x1  0x2  send\n"
	s := Parse(report)
	if s.TotalPorts != 1 || s.SendRights != 1 {
		t.Errorf("header/warning not ignored: %+v", s)
	}
}

func TestParseLeakNote(t *testing.T) {
	var b strings.Builder
	for i := 0; i < portLeakThreshold+1; i++ {
		b.WriteString("0x1  0x2  send\n")
	}
	s := Parse(b.String())
	if s.TotalPorts != portLeakThreshold+1 {
		t.Fatalf("total = %d", s.TotalPorts)
	}
	if len(s.Notes) == 0 {
		t.Error("expected a leak note above the threshold")
	}
}

func TestParseEmpty(t *testing.T) {
	if s := Parse(""); s.TotalPorts != 0 {
		t.Errorf("empty report should have no ports, got %+v", s)
	}
}
