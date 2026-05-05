// Package rpc implements a JSON-RPC 2.0 dispatcher for Spectra's daemon.
// Each connection from a client gets its own request/response loop.
// Handlers are registered by method name; unrecognised methods return
// the standard JSON-RPC "method not found" error.
package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
)

// Request is a JSON-RPC 2.0 request object.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response object.
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

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// HandlerFunc is the type of a registered method handler.
// params is the raw JSON params value (may be nil). The handler returns
// any JSON-serializable result or an error.
type HandlerFunc func(params json.RawMessage) (any, error)

// Dispatcher routes JSON-RPC 2.0 requests to registered handlers.
type Dispatcher struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
}

// NewDispatcher returns an empty Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{handlers: make(map[string]HandlerFunc)}
}

// Register registers a handler for the given method name.
func (d *Dispatcher) Register(method string, fn HandlerFunc) {
	d.mu.Lock()
	d.handlers[method] = fn
	d.mu.Unlock()
}

// Serve handles one connection. Each newline-delimited JSON request on
// the connection produces a newline-delimited JSON response.
// The function returns when the connection is closed.
func (d *Dispatcher) Serve(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for scanner.Scan() {
		line := scanner.Bytes()
		resp := d.handle(line)
		_ = enc.Encode(resp)
	}
}

func (d *Dispatcher) handle(raw []byte) Response {
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return Response{
			JSONRPC: "2.0",
			Error:   &RPCError{Code: CodeParseError, Message: "parse error: " + err.Error()},
		}
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: CodeInvalidRequest, Message: "invalid request"},
		}
	}

	d.mu.RLock()
	fn, ok := d.handlers[req.Method]
	d.mu.RUnlock()

	if !ok {
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: CodeMethodNotFound, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}

	result, err := fn(req.Params)
	if err != nil {
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: CodeInternalError, Message: err.Error()},
		}
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// ServeListener accepts connections from ln and serves each in its own
// goroutine until ln is closed or ctx signals done.
func (d *Dispatcher) ServeListener(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// ErrClosed is normal on shutdown.
			if isClosedErr(err) {
				return nil
			}
			return err
		}
		go d.Serve(conn)
	}
}

func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	// net.ErrClosed is available since Go 1.16.
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	return false
}

// DialUnix connects to a Spectra daemon listening on sockPath. The returned
// ReadWriter can be used to send requests and read responses.
func DialUnix(sockPath string) (io.ReadWriteCloser, error) {
	return net.Dial("unix", sockPath)
}
