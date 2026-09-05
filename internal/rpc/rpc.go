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

	"github.com/kaeawc/spectra/internal/jsonrpc"
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

// invalidParamsError marks a handler error as a client-side request-shape
// violation. The dispatcher reports it as CodeInvalidParams rather than
// CodeInternalError, while preserving the handler's original message.
type invalidParamsError struct{ msg string }

func (e *invalidParamsError) Error() string { return e.msg }

// InvalidParams builds a handler error reported to the client as
// CodeInvalidParams (-32602). Use it for invalid or missing request params
// rather than fmt.Errorf, which is reported as an internal error.
func InvalidParams(format string, args ...any) error {
	return &invalidParamsError{msg: fmt.Sprintf(format, args...)}
}

func isInvalidParams(err error) bool {
	var ip *invalidParamsError
	return errors.As(err, &ip)
}

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

// Dispatch invokes the registered handler for method directly, bypassing the
// wire framing. Callers that run methods outside a connection (such as the
// daemon's detached job runner) use this to share one method table with the
// network path.
func (d *Dispatcher) Dispatch(method string, params json.RawMessage) (any, error) {
	d.mu.RLock()
	fn, ok := d.handlers[method]
	d.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("method not found: %s", method)
	}
	return fn(params)
}

// Serve handles one connection. Each newline-delimited JSON request on
// the connection produces a newline-delimited JSON response.
// The function returns when the connection is closed.
//
// Requests are read with jsonrpc.ReadMessage, which grows its buffer to the
// line length rather than capping it at bufio.Scanner's default 64 KiB. Large
// debug requests (e.g. inspect.app.batch or jvm.mbean.invoke argument payloads)
// would otherwise be silently dropped when the connection exceeded that limit.
func (d *Dispatcher) Serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		line, err := jsonrpc.ReadMessage(reader)
		if err != nil {
			return
		}
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
		code := CodeInternalError
		if isInvalidParams(err) {
			code = CodeInvalidParams
		}
		return Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: code, Message: err.Error()},
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

// DialNetwork connects to a Spectra daemon over any net.Dial-supported
// network/address pair. It is used by remote clients once a daemon is
// intentionally listening beyond its local Unix socket.
func DialNetwork(network, address string) (io.ReadWriteCloser, error) {
	return net.Dial(network, address)
}
