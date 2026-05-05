package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
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
}

// NewDispatcher returns an empty Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{handlers: make(map[string]HandlerFunc)}
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
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: -32700, Message: "parse error"},
		}
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32600, Message: "invalid request"}}
	}

	d.mu.RLock()
	fn, ok := d.handlers[req.Method]
	d.mu.RUnlock()

	if !ok {
		return Response{
			JSONRPC: "2.0", ID: req.ID,
			Error: &RPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
	result, err := fn(uid, req.Params)
	if err != nil {
		return Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: -32603, Message: err.Error()}}
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: result}
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
