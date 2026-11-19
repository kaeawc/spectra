package nettop

import (
	"testing"
	"time"
)

// A reused socket key whose in counter fell but whose out counter grew is a
// new socket: both counters count in full.
func TestSocketReuseResetsBothDirections(t *testing.T) {
	s := newScript(at(c(10, 10000, 1000)), at(c(10, 100, 2000)))
	rep := Summarize(runScript(t, s.config(time.Second, time.Second)), nil, 0)
	p := findProc(rep, 10)
	if p == nil || p.BytesIn != 100 || p.BytesOut != 2000 {
		t.Fatalf("process = %+v, want in 100 out 2000", p)
	}
}

func TestRemotesBasisFor(t *testing.T) {
	if RemotesBasisFor(BasisNettop) != RemotesEndSnapshot || RemotesBasisFor(BasisSSTCP) != RemotesBusiestSockets {
		t.Fatal("wrong remotes basis")
	}
}
