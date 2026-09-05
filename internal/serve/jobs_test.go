package serve

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func blockingDispatch(release <-chan struct{}, result any, err error) jobDispatchFunc {
	return func(string, json.RawMessage) (any, error) {
		<-release
		return result, err
	}
}

func TestJobManagerStartAndWait(t *testing.T) {
	release := make(chan struct{})
	m := newJobManager(blockingDispatch(release, map[string]any{"ok": true}, nil))

	id, err := m.Start("processes.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := m.Get(id, 0)
	if !ok || rec.State != jobStateRunning {
		t.Fatalf("job = %+v, ok=%t, want running", rec, ok)
	}
	if rec.Result != nil {
		t.Fatalf("running job leaked result: %v", rec.Result)
	}

	close(release)
	rec, ok = m.Get(id, 5*time.Second)
	if !ok || rec.State != jobStateDone {
		t.Fatalf("job = %+v, ok=%t, want done", rec, ok)
	}
	if rec.FinishedAt == nil || rec.Result == nil {
		t.Fatalf("done job missing finish data: %+v", rec)
	}
}

func TestJobManagerFailure(t *testing.T) {
	release := make(chan struct{})
	close(release)
	m := newJobManager(blockingDispatch(release, nil, fmt.Errorf("collector exploded")))

	id, err := m.Start("storage.system", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := m.Get(id, 5*time.Second)
	if !ok || rec.State != jobStateFailed {
		t.Fatalf("job = %+v, want failed", rec)
	}
	if !strings.Contains(rec.Error, "collector exploded") {
		t.Fatalf("job error = %q", rec.Error)
	}
}

func TestJobManagerRecoversPanic(t *testing.T) {
	m := newJobManager(func(string, json.RawMessage) (any, error) {
		panic("boom")
	})
	id, err := m.Start("host.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := m.Get(id, 5*time.Second)
	if !ok || rec.State != jobStateFailed || !strings.Contains(rec.Error, "boom") {
		t.Fatalf("job = %+v, want failed with panic message", rec)
	}
}

func TestJobManagerRejectsJobMethodsAndEmpty(t *testing.T) {
	m := newJobManager(func(string, json.RawMessage) (any, error) { return nil, nil })
	if _, err := m.Start("job.start", nil); err == nil {
		t.Fatal("expected error for recursive job method")
	}
	if _, err := m.Start("  ", nil); err == nil {
		t.Fatal("expected error for empty method")
	}
}

func TestJobManagerUnknownJob(t *testing.T) {
	m := newJobManager(func(string, json.RawMessage) (any, error) { return nil, nil })
	if _, ok := m.Get("job-404-dead", 0); ok {
		t.Fatal("expected unknown job to report !ok")
	}
}

func TestJobManagerListSortsNewestFirstAndPrunes(t *testing.T) {
	now := time.Now()
	m := newJobManager(func(string, json.RawMessage) (any, error) { return "ok", nil })
	m.now = func() time.Time { return now }

	var ids []string
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second)
		id, err := m.Start("host.get", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if rec, _ := m.Get(id, 5*time.Second); rec.State != jobStateDone {
			t.Fatalf("job %s did not finish", id)
		}
	}

	list := m.List()
	if len(list) != 3 {
		t.Fatalf("len(list) = %d, want 3", len(list))
	}
	if list[0].ID != ids[2] || list[2].ID != ids[0] {
		t.Fatalf("list order = %v, want newest first", []string{list[0].ID, list[1].ID, list[2].ID})
	}

	// Advance past retention: finished jobs are pruned.
	now = now.Add(defaultJobRetention + time.Minute)
	if got := len(m.List()); got != 0 {
		t.Fatalf("after retention len(list) = %d, want 0", got)
	}
}

func TestJobManagerCapacityDropsOldestFinishedOnly(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	blocked := map[string]bool{}
	m := newJobManager(func(method string, _ json.RawMessage) (any, error) {
		if method == "slow.collector" {
			mu.Lock()
			blocked[method] = true
			mu.Unlock()
			<-release
		}
		return "ok", nil
	})
	m.capacity = 2

	runningID, err := m.Start("slow.collector", nil)
	if err != nil {
		t.Fatal(err)
	}
	doneID, err := m.Start("host.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.Get(doneID, 5*time.Second); rec.State != jobStateDone {
		t.Fatal("fast job did not finish")
	}
	newID, err := m.Start("host.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.Get(newID, 5*time.Second); rec.State != jobStateDone {
		t.Fatal("third job did not finish")
	}

	// Trigger pruning: capacity 2 with 3 jobs drops the oldest finished job,
	// never the running one.
	m.List()
	if _, ok := m.Get(runningID, 0); !ok {
		t.Fatal("running job was pruned")
	}
	if _, ok := m.Get(doneID, 0); ok {
		t.Fatal("oldest finished job survived pruning past capacity")
	}
	close(release)
	if rec, _ := m.Get(runningID, 5*time.Second); rec.State != jobStateDone {
		t.Fatal("running job did not finish after release")
	}
}
