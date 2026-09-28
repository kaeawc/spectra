package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const ProtocolVersion = "spectra-daemon/1"

const (
	MethodStatus       = "daemon.status"
	MethodShutdown     = "daemon.shutdown"
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

type Request struct {
	Method  string
	Params  json.RawMessage
	PeerUID uint32
	Notify  func(method string, params any) error
	Done    <-chan struct{}
}

type Handler func(context.Context, *Request) (any, error)

type invalidParamsError struct{ error }

func InvalidParams(format string, args ...any) error {
	return invalidParamsError{fmt.Errorf(format, args...)}
}

type StatusResult struct {
	Protocol      string    `json:"protocol"`
	Version       string    `json:"version"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	Socket        string    `json:"socket"`
	Methods       []string  `json:"methods"`
}
