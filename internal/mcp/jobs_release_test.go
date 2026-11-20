package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestJobStartRefusesACancelledRequest(t *testing.T) {
	srv := reportTestServer(t, new(int))
	srv.jobs = newJobStore(jobConfig{}, nil)
	defer srv.jobs.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := srv.jobTool(ctx, "job_start", "s", json.RawMessage(`{"tool":"triage","arguments":{}}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, context.Canceled.Error()) {
		t.Fatalf("job_start under a cancelled request = %+v, want refused", res)
	}
	if n := len(srv.jobs.list("s").Jobs); n != 0 {
		t.Fatalf("a cancelled request registered %d job(s)", n)
	}
	h := srv.jobToolHandler("job_start")
	if _, err := h(ctx, &sdk.CallToolRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler under a cancelled request: %v, want context.Canceled", err)
	}
}

func TestAcceptedJobOutlivesItsRequest(t *testing.T) {
	st := newJobStore(jobConfig{}, nil)
	defer st.close()
	b := &blockingHandler{release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	rec, err := st.startCtx(ctx, "s", "spawns", "", nil, b.handle)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job running", func() bool { return b.running.Load() == 1 })
	cancel()
	close(b.release)
	waitFor(t, "job completion", func() bool { return storeStatus(t, st, "s", rec.JobID).Status == jobCompleted })
	if b.ctxDone.Load() != 0 {
		t.Fatal("cancelling the request cancelled the accepted job")
	}
}

func jobHolds(st *jobStore, id string) (args, run bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	j := st.jobs[id]
	return j.args != nil, j.run != nil
}

func TestJobsReleaseArgumentsOnceRunning(t *testing.T) {
	st := newJobStore(jobConfig{MaxConcurrent: 1}, nil)
	defer st.close()
	b := &blockingHandler{release: make(chan struct{})}
	args := json.RawMessage(`{"padding":"` + strings.Repeat("x", 4096) + `"}`)
	running, _ := st.start("s", "spawns", "", args, b.handle)
	queued, _ := st.start("s", "spawns", "", args, b.handle)
	waitFor(t, "first job running", func() bool { return b.running.Load() == 1 })
	if a, r := jobHolds(st, running.JobID); a || r {
		t.Fatalf("running job holds args=%v run=%v; its worker has them", a, r)
	}
	if a, r := jobHolds(st, queued.JobID); !a || !r {
		t.Fatalf("queued job holds args=%v run=%v; it still needs both", a, r)
	}
	if _, err := st.cancelJob("s", queued.JobID); err != nil {
		t.Fatal(err)
	}
	if a, r := jobHolds(st, queued.JobID); a || r {
		t.Fatalf("cancelled queued job holds args=%v run=%v", a, r)
	}
	close(b.release)
	waitFor(t, "first job done", func() bool { return storeStatus(t, st, "s", running.JobID).Status == jobCompleted })
	if a, r := jobHolds(st, running.JobID); a || r {
		t.Fatalf("finished job holds args=%v run=%v", a, r)
	}
}

func TestJobArgumentsAreBounded(t *testing.T) {
	st := newJobStore(jobConfig{MaxArgsBytes: 64}, nil)
	defer st.close()
	b := &blockingHandler{release: make(chan struct{})}
	close(b.release)
	big := json.RawMessage(`{"padding":"` + strings.Repeat("x", 64) + `"}`)
	if _, err := st.start("s", "spawns", "", big, b.handle); err == nil || !strings.Contains(err.Error(), "64-byte job limit") {
		t.Fatalf("oversized arguments: %v, want refused", err)
	}
	if _, err := st.start("s", "spawns", "", json.RawMessage(`{}`), b.handle); err != nil {
		t.Fatalf("small arguments: %v", err)
	}
}

func TestJobCancelWhileQueuedLeavesTheQueue(t *testing.T) {
	st := newJobStore(jobConfig{MaxConcurrent: 1}, nil)
	defer st.close()
	b := &blockingHandler{release: make(chan struct{})}
	defer close(b.release)
	if _, err := st.start("s", "spawns", "", nil, b.handle); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 3 {
		r, err := st.start("s", "spawns", "", nil, b.handle)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.JobID)
	}
	waitFor(t, "first job running", func() bool { return b.running.Load() == 1 })
	if _, err := st.cancelJob("s", ids[1]); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.queue) != 2 || st.queue[0].id != ids[0] || st.queue[1].id != ids[2] {
		t.Fatalf("queue after cancelling the middle job has %d entries, want the other two in order", len(st.queue))
	}
	if tail := st.queue[:3][2]; tail != nil {
		t.Fatal("the vacated queue slot still references the cancelled job")
	}
}
