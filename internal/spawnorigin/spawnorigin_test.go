package spawnorigin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

func TestObserveMatchesStartExactly(t *testing.T) {
	s := NewStore(Options{UID: 501})
	a := p(10, 1, "x", 0)
	s.Observe(at(1), []Proc{a})
	s.Observe(at(2), []Proc{a})
	if s.Len() != 1 {
		t.Fatalf("same lifetime split: %d records", s.Len())
	}
	// A different start is a different lifetime; Lookup still tolerates ps jitter.
	a.Start = a.Start.Add(time.Second)
	s.Observe(at(3), []Proc{a})
	if s.Len() != 2 {
		t.Fatalf("records = %d, want 2", s.Len())
	}
	if _, _, ok := s.Lookup(10, at(0)); !ok {
		t.Fatal("lookup lost its tolerance")
	}
}

func TestMissedProbeRevivesInsteadOfDuplicating(t *testing.T) {
	s := NewStore(Options{UID: 501})
	a := p(10, 1, "x", 0)
	s.Observe(at(1), []Proc{a})
	s.Observe(at(2), nil) // probe missed it
	s.Observe(at(3), []Proc{a})
	if s.Len() != 1 || len(s.byPID[10]) != 1 {
		t.Fatalf("records = %d byPID = %v", s.Len(), s.byPID[10])
	}
	rec, _, _ := s.Lookup(10, a.Start)
	if !rec.ExitedAt.IsZero() || !rec.LastSeen.Equal(at(3)) {
		t.Fatalf("record = %+v", rec)
	}
}

func TestEvictsLeavesBeforeLiveAncestors(t *testing.T) {
	s := NewStore(Options{UID: 501, MaxRecords: 4})
	agent, shell := p(10, 1, "agent", -100), p(11, 10, "zsh", -90)
	procs := []Proc{agent, shell}
	s.Observe(at(0), procs)
	for i := range 10 {
		procs = append(procs, p(100+i, 11, "tick", 1+i))
		s.Observe(at(1+i), procs)
	}
	if s.Len() != 4 {
		t.Fatalf("records = %d", s.Len())
	}
	for _, q := range []Proc{agent, shell} {
		if _, _, ok := s.Lookup(q.PID, q.Start); !ok {
			t.Fatalf("live ancestor %s evicted", q.Comm)
		}
	}
}

func TestTruncateIsByteBoundedAndUTF8Safe(t *testing.T) {
	in := strings.Repeat("é", 400) // 800 bytes
	got := truncate(in, maxArgvBytes)
	if len(got) > maxArgvBytes || !utf8.ValidString(got) || len(got) != 512 {
		t.Fatalf("len=%d valid=%v", len(got), utf8.ValidString(got))
	}
	got = truncate("a"+strings.Repeat("é", 10), 4) // cut would land mid-rune
	if !utf8.ValidString(got) || got != "aé" {
		t.Fatalf("got %q", got)
	}
}

func TestAnswerDedupesQueriesAndSharesAncestors(t *testing.T) {
	s := NewStore(Options{UID: 501})
	procs := []Proc{p(10, 1, "a", -5)}
	for i := range 20 {
		procs = append(procs, p(11+i, 10, "c", -1))
	}
	s.Observe(at(0), procs)
	var qs []Query
	for range 1000 {
		qs = append(qs, Query{PID: 11, StartTime: at(-1)})
	}
	for i := range 20 {
		qs = append(qs, Query{PID: 11 + i, StartTime: at(-1)})
	}
	resp := s.Answer(qs)
	if len(resp.Answers) != 20 {
		t.Fatalf("answers = %d, want 20 unique", len(resp.Answers))
	}
	if len(resp.Ancestors) != 1 {
		t.Fatalf("shared table = %d, want 1", len(resp.Ancestors))
	}
	raw, _ := json.Marshal(resp)
	var back Response
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Resolve()
	for _, a := range back.Answers {
		if !a.Found || len(a.Chain) != 1 || a.Chain[0].PID != 10 {
			t.Fatalf("answer = %+v", a)
		}
	}
}

func TestAnswerCapsUniqueQueries(t *testing.T) {
	s := NewStore(Options{UID: 501})
	var qs []Query
	for i := range MaxQueries + 5 {
		qs = append(qs, Query{PID: i + 1, StartTime: at(-1)})
	}
	resp := s.Answer(qs)
	if len(resp.Answers) != MaxQueries || !resp.Truncated {
		t.Fatalf("answers = %d truncated = %v", len(resp.Answers), resp.Truncated)
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
	resp.Resolve()
	if !resp.RecordingSince.Equal(at(0)) || resp.Records != 2 || resp.MaxRecords != DefaultMaxRecords || len(resp.Answers) != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	if !resp.Answers[0].Found || len(resp.Answers[0].Chain) != 1 || resp.Answers[1].Found || resp.Answers[2].Found {
		t.Fatalf("answers = %+v", resp.Answers)
	}
}
