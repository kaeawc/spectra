// Package jsonrpc implements newline-delimited JSON-RPC 2.0 transport primitives,
// matching the MCP stdio transport: each message is a single JSON object on its
// own line, terminated by '\n'. Embedded newlines are not permitted in payloads.
package jsonrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"sync"

	"github.com/kaeawc/spectra/internal/logger"
)

// pkgLog routes transport errors when message marshaling fails.
var pkgLog logger.Logger = logger.New(logger.Config{Format: logger.FormatText, Level: slog.LevelInfo})

// SetLogger replaces the package-level logger used by WriteMessage transport helpers.
func SetLogger(l logger.Logger) { pkgLog = l }

// Request is a JSON-RPC 2.0 request or notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result"`
	Error   *Error      `json:"error,omitempty"`
}

type successResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result"`
}

type errorResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Error   *Error      `json:"error"`
}

// Error represents JSON-RPC transport/application errors.
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Notification is a one-way JSON-RPC notification.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// ReadMessage reads a single newline-delimited JSON-RPC body. Blank lines are
// skipped so stray whitespace from a peer does not abort the loop.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return nil, err
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		return trimmed, nil
	}
}

// WriteMessage serializes msg as one newline-terminated JSON object.
func WriteMessage(w io.Writer, mu *sync.Mutex, msg any) {
	payload, err := json.Marshal(msg)
	if err != nil {
		pkgLog.Error("jsonrpc marshal failed", "err", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, err := w.Write(payload); err != nil {
		pkgLog.Error("jsonrpc write body failed", "err", err)
		return
	}
	if _, err := w.Write([]byte{'\n'}); err != nil {
		pkgLog.Error("jsonrpc write newline failed", "err", err)
	}
}

// SendResponse writes a JSON-RPC response with either result or error.
func SendResponse(w io.Writer, mu *sync.Mutex, id interface{}, result interface{}, rpcErr *Error) {
	var msg interface{}
	if rpcErr != nil {
		msg = errorResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   rpcErr,
		}
	} else {
		msg = successResponse{
			JSONRPC: "2.0",
			ID:      id,
			Result:  result,
		}
	}
	WriteMessage(w, mu, msg)
}

// SendNotification writes a JSON-RPC notification.
func SendNotification(w io.Writer, mu *sync.Mutex, method string, params interface{}) {
	msg := Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	WriteMessage(w, mu, msg)
}
