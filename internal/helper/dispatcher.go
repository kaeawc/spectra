package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// HandlerFunc handles one method call.
type HandlerFunc func(callerUID uint32, params json.RawMessage) (any, error)

// Dispatcher routes length-framed JSON-RPC 2.0 requests.
type Dispatcher struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
	auditMu  sync.Mutex
	audit    io.Writer
	now      func() time.Time
}

// NewDispatcher returns an empty Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{handlers: make(map[string]HandlerFunc), now: time.Now}
}

// Register adds a handler for method. Panics if method already registered.
func (d *Dispatcher) Register(method string, fn HandlerFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.handlers[method]; ok {
		panic("helper: duplicate method: " + method)
	}
	d.handlers[method] = fn
}

// SetAuditWriter enables JSON-lines audit logging for handled requests.
func (d *Dispatcher) SetAuditWriter(w io.Writer) {
	d.auditMu.Lock()
	defer d.auditMu.Unlock()
	d.audit = w
}

// SetClock injects the clock used by audit logging.
func (d *Dispatcher) SetClock(now func() time.Time) {
	d.auditMu.Lock()
	defer d.auditMu.Unlock()
	if now == nil {
		d.now = time.Now
		return
	}
	d.now = now
}

// Serve handles one connection using the length-framed protocol.
func (d *Dispatcher) Serve(conn net.Conn) {
	defer conn.Close()
	uid := peerUID(conn)
	for {
		raw, err := ReadMessage(conn)
		if err != nil {
			return
		}
		resp := d.handle(uid, raw)
		payload, err := json.Marshal(resp)
		if err != nil {
			return
		}
		if err := WriteMessage(conn, payload); err != nil {
			return
		}
	}
}

func (d *Dispatcher) handle(uid uint32, raw []byte) Response {
	start := d.now()
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		d.auditRequest(start, uid, "", false, "parse error")
		return Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32700, Message: "parse error"},
		}
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		d.auditRequest(start, uid, req.Method, false, "invalid request")
		return Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32600, Message: "invalid request"}}
	}

	d.mu.RLock()
	fn, ok := d.handlers[req.Method]
	d.mu.RUnlock()

	if !ok {
		d.auditRequest(start, uid, req.Method, false, "method not found")
		return Response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &RPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
	result, err := fn(uid, req.Params)
	if err != nil {
		d.auditRequest(start, uid, req.Method, false, err.Error())
		return Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32603, Message: err.Error()}}
	}
	d.auditRequest(start, uid, req.Method, true, "")
	return Response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

type auditEvent struct {
	Time       string `json:"time"`
	UID        uint32 `json:"uid"`
	Method     string `json:"method"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func (d *Dispatcher) auditRequest(start time.Time, uid uint32, method string, ok bool, msg string) {
	d.auditMu.Lock()
	defer d.auditMu.Unlock()
	if d.audit == nil {
		return
	}
	event := auditEvent{
		Time:       start.UTC().Format(time.RFC3339Nano),
		UID:        uid,
		Method:     method,
		OK:         ok,
		DurationMS: d.now().Sub(start).Milliseconds(),
		Error:      msg,
	}
	_ = json.NewEncoder(d.audit).Encode(event)
}

// ServeListener accepts connections and serves each in its own goroutine.
func (d *Dispatcher) ServeListener(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go d.Serve(conn)
	}
}
