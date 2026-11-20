package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitRunDone(t *testing.T, r *wireSession, within time.Duration) {
	t.Helper()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(within):
		t.Fatalf("Run did not return within %s of EOF", within)
	}
}

func TestFinalLineWithoutNewlineIsAnswered(t *testing.T) {
	srv := NewServer(nil, nil)
	srv.eofWait = time.Minute
	r := startWireSession(t, srv)
	if _, err := io.WriteString(r.in, `{"jsonrpc":"2.0","id":"last","method":"ping"}`); err != nil {
		t.Fatal(err)
	}
	_ = r.in.Close()
	r.reply("last")
	waitRunDone(t, r, 10*time.Second)
}

func TestIDKeyMatchesSDKNormalisation(t *testing.T) {
	same := [][2]string{{`5`, `5.0`}, {`5`, ` 5 `}, {`"a"`, `"\u0061"`}}
	for _, pair := range same {
		a, b := idKey(json.RawMessage(pair[0])), idKey(json.RawMessage(pair[1]))
		if a == "" || a != b {
			t.Errorf("idKey(%s) = %q, idKey(%s) = %q; want one non-empty key", pair[0], a, pair[1], b)
		}
	}
	if idKey(json.RawMessage(`5`)) == idKey(json.RawMessage(`"5"`)) {
		t.Error("number 5 and string \"5\" share a key")
	}
	for _, raw := range []string{``, `null`, `{}`, `[1]`, `true`} {
		if k := idKey(json.RawMessage(raw)); k != "" {
			t.Errorf("idKey(%s) = %q, want none", raw, k)
		}
	}
}

func TestCancelledFloatIDDropsNormalisedReply(t *testing.T) {
	tr := newReplyTracker()
	var line bytes.Buffer
	line.WriteString(`{"jsonrpc":"2.0","id":5.0,"method":"tools/call","params":{}}` + "\n")
	line.WriteString(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5.0}}` + "\n")
	if _, err := io.ReadAll(newTrackingReader(context.Background(), &line, tr, 0)); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	w := &replyWriter{w: &out, t: tr}
	// The SDK replies with the id it normalised: 5, not 5.0.
	if _, err := w.Write([]byte(`{"jsonrpc":"2.0","id":5,"result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %q for a cancelled call", out.String())
	}
	if len(tr.pending) != 0 {
		t.Fatalf("call still tracked after its reply: %v", tr.pending)
	}
}

func TestEscapedStringIDReplyClosesCall(t *testing.T) {
	srv := NewServer(nil, nil)
	srv.eofWait = time.Minute
	r := startWireSession(t, srv)
	r.send(`{"jsonrpc":"2.0","id":"\u0061","method":"ping"}`)
	r.reply("a")
	_ = r.in.Close()
	waitRunDone(t, r, 10*time.Second)
}

func TestLinesOverTrackedLineMaxPassThroughUntracked(t *testing.T) {
	long := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"pad":"` + strings.Repeat("x", trackedLineMax) + `"}}` + "\n"
	short := `{"jsonrpc":"2.0","id":8,"method":"ping"}` + "\n"
	in := long + short + long
	tr := newReplyTracker()
	got, err := io.ReadAll(newTrackingReader(context.Background(), strings.NewReader(in), tr, 0))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != in {
		t.Fatalf("reader changed the input: got %d bytes, want %d", len(got), len(in))
	}
	if len(tr.pending) != 1 {
		t.Fatalf("pending = %v, want only the short call", tr.pending)
	}
	if _, ok := tr.pending[idKey(json.RawMessage(`8`))]; !ok {
		t.Fatalf("the line after a long one was not tracked: %v", tr.pending)
	}
}

// blockingSampler is a process collector whose sample runs until its call's
// context ends.
type blockingSampler struct {
	countingProcessCollector
	started   chan struct{}
	cancelled atomic.Bool
}

func (b *blockingSampler) SampleProcess(ctx context.Context, _, _, _ int) (string, error) {
	close(b.started)
	select {
	case <-ctx.Done():
		b.cancelled.Store(true)
		return "", ctx.Err()
	case <-time.After(time.Minute):
		return "finished", nil
	}
}

func TestCancelledSampleReleasesCallSlot(t *testing.T) {
	sampler := &blockingSampler{started: make(chan struct{})}
	r := startWireSession(t, lifecycleServer(newBlockingSpawnDeps(), sampler))
	r.call("sample", "process", `{"operation":"sample","pid":20,"duration_seconds":60}`)
	waitClosed(t, sampler.started, "sample to start")
	r.cancel("sample")
	r.call("after", "lineage", `{"pid":20}`)
	if res := r.reply("after"); res["error"] != nil {
		t.Fatalf("call after the cancelled sample failed: %v", res)
	}
	if !sampler.cancelled.Load() {
		t.Fatal("the sample's context was not cancelled")
	}
}
