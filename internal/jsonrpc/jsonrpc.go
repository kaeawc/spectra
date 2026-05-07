// Package jsonrpc implements Content-Length-framed JSON-RPC 2.0 transport primitives.
package jsonrpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
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

// ReadMessage reads a single JSON-RPC body from Content-Length framing.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	var contentLength int
	headerDone := false

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			headerDone = true
			break
		}
		if strings.HasPrefix(line, "Content-Length: ") {
			trim := strings.TrimPrefix(line, "Content-Length: ")
			contentLength, err = strconv.Atoi(trim)
			if err != nil {
				return nil, fmt.Errorf("parse Content-Length %q: %w", trim, err)
			}
		}
	}
	if !headerDone {
		return nil, fmt.Errorf("unexpected EOF while reading headers")
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("invalid content-length %d", contentLength)
	}
	body := make([]byte, contentLength)
	_, err := io.ReadFull(r, body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// WriteMessage serializes and writes one Message with Content-Length framing.
func WriteMessage(w io.Writer, mu *sync.Mutex, msg any) {
	payload, err := json.Marshal(msg)
	if err != nil {
		pkgLog.Error("jsonrpc marshal failed", "err", err)
		return
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	mu.Lock()
	defer mu.Unlock()
	if _, err := w.Write([]byte(header)); err != nil {
		pkgLog.Error("jsonrpc write header failed", "err", err)
		return
	}
	if _, err := w.Write(payload); err != nil {
		pkgLog.Error("jsonrpc write body failed", "err", err)
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
