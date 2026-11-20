package spawnorigin

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func p(pid, ppid int, comm string, startSec int) Proc {
	return Proc{PID: pid, PPID: ppid, UID: 501, Comm: comm, Start: at(startSec).Add(300 * time.Millisecond)}
}

func TestParentExitWhileRecording(t *testing.T) {
	s := NewStore(Options{UID: 501})
	agent, shell := p(100, 1, "claude", -600), p(200, 100, "zsh", -60)
	s.Observe(at(0), []Proc{agent, shell})
	// The shell starts a server, then the shell exits and the server is
	// reparented to pid 1.
	server := p(300, 200, "node", 3)
	s.Observe(at(5), []Proc{agent, shell, server})
	for _, k := range s.PendingDetail(10) {
		s.SetDetail(k, map[int]string{100: "claude --lane l-1", 200: "/bin/zsh -c npm start"}[k.PID], "/w/l-1")
	}
	server.PPID = 1
	s.Observe(at(10), []Proc{agent, server})

	// ps reports start times truncated to the second.
	rec, anc, ok := s.Lookup(300, at(3))
	if !ok || rec.PPID != 200 || rec.ParentComm != "zsh" || rec.Baseline {
		t.Fatalf("record = %+v ok=%v", rec, ok)
	}
	if len(anc) != 2 || anc[0].PID != 200 || !anc[0].ExitedAt.Equal(at(10)) || anc[0].Argv != "/bin/zsh -c npm start" || anc[0].Cwd != "/w/l-1" {
		t.Fatalf("parent = %+v", anc)
	}
	if anc[1].PID != 100 || !anc[1].ExitedAt.IsZero() || anc[1].Argv != "claude --lane l-1" {
		t.Fatalf("grandparent = %+v", anc[1])
	}
}

func TestPIDReuseDoesNotInheritOrigin(t *testing.T) {
	s := NewStore(Options{UID: 501})
	s.Observe(at(0), []Proc{p(50, 1, "make", -10), p(60, 50, "worker", -5)})
	s.Observe(at(5), nil) // both exit
	// pid 60 is reused by an unrelated process.
	s.Observe(at(10), []Proc{p(70, 1, "launcher", 6), p(60, 70, "other", 8)})
	rec, anc, ok := s.Lookup(60, at(8))
	if !ok || rec.PPID != 70 || rec.ParentComm != "launcher" {
		t.Fatalf("reused pid got %+v ok=%v", rec, ok)
	}
	if len(anc) != 1 || anc[0].PID != 70 {
		t.Fatalf("ancestors = %+v", anc)
	}
	// A query for a lifetime the store never saw finds nothing.
	if _, _, ok := s.Lookup(60, at(60)); ok {
		t.Fatal("unknown lifetime matched")
	}
	// Parent pid reuse: a new pid 50 must not be taken for worker's parent.
	s.Observe(at(15), []Proc{p(70, 1, "launcher", 6), p(60, 70, "other", 8), p(50, 1, "unrelated", 12)})
	if _, anc, _ := s.Lookup(60, at(-5)); len(anc) != 1 || anc[0].Comm != "make" {
		t.Fatalf("old worker ancestors = %+v", anc)
	}
}

func TestStartJitterKeepsOneLifetime(t *testing.T) {
	s := NewStore(Options{UID: 501})
	a := p(10, 1, "x", 0)
	s.Observe(at(1), []Proc{a})
	a.Start = a.Start.Add(time.Second) // btime drift
	s.Observe(at(2), []Proc{a})
	if s.Len() != 1 {
		t.Fatalf("jitter split the lifetime: %d records", s.Len())
	}
}

func TestOtherUsersIgnored(t *testing.T) {
	s := NewStore(Options{UID: 501})
	root := Proc{PID: 5, PPID: 1, UID: 0, Comm: "sshd", Start: at(-100)}
	s.Observe(at(0), []Proc{root, p(6, 5, "zsh", -50), {PID: 7, PPID: 6, UID: 501, Comm: "nostart"}})
	if s.Len() != 1 {
		t.Fatalf("records = %d", s.Len())
	}
	rec, _, _ := s.Lookup(6, at(-50))
	if rec.ParentComm != "sshd" || !rec.Baseline {
		t.Fatalf("record = %+v", rec)
	}
}

func TestBoundEvictsUnneededExitedFirst(t *testing.T) {
	s := NewStore(Options{UID: 501, MaxRecords: 4})
	parent, orphan := p(10, 1, "agent", -100), p(11, 10, "server", -50)
	s.Observe(at(0), []Proc{parent, orphan})
	orphan.PPID = 1
	s.Observe(at(1), []Proc{orphan}) // parent exits, orphan lives on
	for i := range 20 {
		s.Observe(at(2+i), []Proc{orphan, p(100+i, 11, "tick", 2+i)})
	}
	if s.Len() > 4 {
		t.Fatalf("bound not enforced: %d", s.Len())
	}
	if _, anc, ok := s.Lookup(11, at(-50)); !ok || len(anc) != 1 || anc[0].Comm != "agent" {
		t.Fatalf("orphan's exited parent evicted before short-lived records: %+v", anc)
	}
	if _, _, ok := s.Lookup(100, at(2)); ok {
		t.Fatal("oldest short-lived record survived")
	}
}

func TestBoundEvictsLiveWhenFull(t *testing.T) {
	s := NewStore(Options{UID: 501, MaxRecords: 2})
	s.Observe(at(0), []Proc{p(1001, 1, "a", 0), p(1002, 1, "b", 0), p(1003, 1, "c", 0)})
	if s.Len() != 2 {
		t.Fatalf("records = %d", s.Len())
	}
}

func TestExitedTTL(t *testing.T) {
	s := NewStore(Options{UID: 501, ExitedTTL: time.Minute})
	s.Observe(at(0), []Proc{p(10, 1, "a", -1)})
	s.Observe(at(1), nil)
	s.Observe(at(30), nil)
	if s.Len() != 1 {
		t.Fatalf("pruned too early: %d", s.Len())
	}
	s.Observe(at(62), nil)
	if s.Len() != 0 {
		t.Fatalf("TTL not applied: %d", s.Len())
	}
}

func TestPendingDetailLimitAndOnce(t *testing.T) {
	s := NewStore(Options{UID: 501})
	var procs []Proc
	for i := range 5 {
		procs = append(procs, p(10+i, 1, "parent", 0), p(100+i, 10+i, "child", 1))
	}
	s.Observe(at(2), procs)
	keys := s.PendingDetail(3)
	if len(keys) != 3 {
		t.Fatalf("pending = %v", keys)
	}
	for _, k := range keys {
		s.SetDetail(k, "", "") // unreadable still counts as read
	}
	if rest := s.PendingDetail(10); len(rest) != 2 {
		t.Fatalf("rest = %v", rest)
	}
}

func TestAnswer(t *testing.T) {
	s := NewStore(Options{UID: 501})
	s.Observe(at(0), []Proc{p(10, 1, "a", -5), p(11, 10, "b", -1)})
	resp := s.Answer([]Query{{PID: 11, StartTime: at(-1)}, {PID: 11, StartTime: at(-100)}, {PID: 12}})
	if !resp.RecordingSince.Equal(at(0)) || resp.Records != 2 || resp.MaxRecords != DefaultMaxRecords || len(resp.Answers) != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	if !resp.Answers[0].Found || len(resp.Answers[0].Ancestors) != 1 || resp.Answers[1].Found || resp.Answers[2].Found {
		t.Fatalf("answers = %+v", resp.Answers)
	}
}
