package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// The SDK's stdio transport cancels every in-flight call as soon as its input
// reaches EOF, and still writes a reply for a call the client cancelled. The
// wrappers here sit between the raw streams and the transport: the reader
// notes each incoming request and cancellation before the SDK sees it and
// holds back EOF until the open calls have replied; the writer notes each
// reply and drops the ones the client cancelled.

// trackedLineMax bounds one buffered input line. Longer lines (only very
// large tool arguments) pass through untracked: a cancellation of such a call
// does not drop its reply, and EOF does not wait for it.
const trackedLineMax = 1 << 20

// replyTracker records the JSON-RPC calls that are still owed a reply.
type replyTracker struct {
	mu      sync.Mutex
	pending map[string]bool // request id -> cancelled by the client
	changed chan struct{}
}

func newReplyTracker() *replyTracker {
	return &replyTracker{pending: map[string]bool{}, changed: make(chan struct{}, 1)}
}

func (t *replyTracker) signal() {
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

func (t *replyTracker) request(id string) {
	t.mu.Lock()
	t.pending[id] = false
	t.mu.Unlock()
}

// cancel marks an open call as cancelled: its reply will be dropped and EOF
// no longer waits for it.
func (t *replyTracker) cancel(id string) {
	t.mu.Lock()
	if _, ok := t.pending[id]; ok {
		t.pending[id] = true
	}
	t.mu.Unlock()
	t.signal()
}

// cancelled reports whether the reply to id must be dropped.
func (t *replyTracker) cancelled(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending[id]
}

// replied closes the call id.
func (t *replyTracker) replied(id string) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
	t.signal()
}

// tracking reports whether any call, cancelled or not, is still open.
func (t *replyTracker) tracking() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending) > 0
}

func (t *replyTracker) open() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, cancelled := range t.pending {
		if !cancelled {
			n++
		}
	}
	return n
}

// waitIdle blocks until no uncancelled call is open, timeout passes, or ctx
// ends.
func (t *replyTracker) waitIdle(ctx context.Context, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for t.open() > 0 {
		select {
		case <-t.changed:
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// wireMessage is the part of a JSON-RPC message the tracker reads.
type wireMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		RequestID json.RawMessage `json:"requestId"`
	} `json:"params"`
}

// splitMessages returns the messages in one frame: a single message or a
// batch array. Anything unparsable yields nothing.
func splitMessages(frame []byte) []json.RawMessage {
	frame = bytes.TrimSpace(frame)
	if len(frame) == 0 {
		return nil
	}
	if frame[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(frame, &batch) != nil {
			return nil
		}
		return batch
	}
	return []json.RawMessage{frame}
}

func decodeWire(raw json.RawMessage) (wireMessage, bool) {
	var m wireMessage
	if json.Unmarshal(raw, &m) != nil {
		return m, false
	}
	return m, true
}

// idKey returns the key for a request id as the SDK will see it: decoded and
// coerced by jsonrpc.MakeID, so 5 and 5.0, or "a" and "\u0061", are one call.
// It returns "" for an absent, null, or invalid id.
func idKey(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	id, err := jsonrpc.MakeID(v)
	if err != nil || !id.IsValid() {
		return ""
	}
	switch v := id.Raw().(type) {
	case int64:
		return "n:" + strconv.FormatInt(v, 10)
	case string:
		return "s:" + v
	}
	return ""
}

// trackingReader passes input through unchanged, line by line.
type trackingReader struct {
	ctx     context.Context
	src     *bufio.Reader
	t       *replyTracker
	eofWait time.Duration
	out     []byte
	long    bool // inside a line too long to track
	err     error
	// atEOF defers the wait for open calls to the Read after the final
	// chunk, so a last request without a trailing newline reaches the SDK
	// before EOF is held back.
	atEOF bool
}

func newTrackingReader(ctx context.Context, src io.Reader, t *replyTracker, eofWait time.Duration) *trackingReader {
	return &trackingReader{ctx: ctx, src: bufio.NewReaderSize(src, trackedLineMax), t: t, eofWait: eofWait}
}

func (r *trackingReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.err != nil {
			if r.atEOF {
				r.atEOF = false
				r.t.waitIdle(r.ctx, r.eofWait)
			}
			return 0, r.err
		}
		chunk, err := r.src.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			r.long = true
			err = nil
		case r.long:
			r.long = false
		default:
			r.observe(chunk)
		}
		r.out = append(r.out[:0], chunk...)
		if err != nil {
			r.err = err
			r.atEOF = errors.Is(err, io.EOF)
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *trackingReader) observe(line []byte) {
	for _, raw := range splitMessages(line) {
		m, ok := decodeWire(raw)
		if !ok || m.Method == "" {
			continue
		}
		if id := idKey(m.ID); id != "" {
			r.t.request(id)
		} else if m.Method == "notifications/cancelled" {
			if id := idKey(m.Params.RequestID); id != "" {
				r.t.cancel(id)
			}
		}
	}
}

// replyWriter drops replies to cancelled calls and closes each call once its
// reply is written. The SDK writes one whole frame per Write.
type replyWriter struct {
	w io.Writer
	t *replyTracker
}

func (w *replyWriter) Write(p []byte) (int, error) {
	if !w.t.tracking() {
		// No call is open, so there is no reply to drop or close.
		return w.w.Write(p)
	}
	msgs := splitMessages(p)
	var replies []string
	var keep []json.RawMessage
	dropped := false
	for _, raw := range msgs {
		m, ok := decodeWire(raw)
		id := idKey(m.ID)
		if !ok || m.Method != "" || id == "" {
			keep = append(keep, raw)
			continue
		}
		replies = append(replies, id)
		if w.t.cancelled(id) {
			dropped = true
			continue
		}
		keep = append(keep, raw)
	}
	defer func() {
		for _, id := range replies {
			w.t.replied(id)
		}
	}()
	if !dropped {
		return w.w.Write(p)
	}
	if len(keep) == 0 {
		return len(p), nil
	}
	frame, err := json.Marshal(keep)
	if err != nil {
		return 0, err
	}
	if _, err := w.w.Write(append(frame, '\n')); err != nil {
		return 0, err
	}
	return len(p), nil
}
