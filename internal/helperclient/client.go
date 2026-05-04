// Package helperclient provides a client for the Spectra privileged helper.
// If the helper is not installed or not running, all methods return a
// sentinel ErrHelperUnavailable so callers can gracefully degrade.
//
// See docs/design/privileged-helper.md for the protocol spec.
package helperclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/kaeawc/spectra/internal/helper"
)

// DefaultSockPath is the canonical Unix socket path for the helper.
const DefaultSockPath = "/var/run/spectra-helper.sock"

// ErrHelperUnavailable is returned when the helper is not installed or
// is not currently running. Callers should degrade gracefully.
var ErrHelperUnavailable = errors.New("helperclient: helper not available")

// Client dials the helper and sends length-framed JSON-RPC 2.0 requests.
// It is safe for concurrent use; each call dials its own connection.
type Client struct {
	sockPath string
	reqID    atomic.Int64
}

// New returns a Client using the default socket path.
func New() *Client { return &Client{sockPath: DefaultSockPath} }

// NewWithPath returns a Client using sockPath (for testing).
func NewWithPath(sockPath string) *Client { return &Client{sockPath: sockPath} }

// Available returns true if the helper socket is reachable.
func (c *Client) Available() bool {
	conn, err := net.DialTimeout("unix", c.sockPath, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// call sends one JSON-RPC request and returns the raw result JSON.
func (c *Client) call(method string, params any) (json.RawMessage, error) {
	conn, err := net.DialTimeout("unix", c.sockPath, 3*time.Second)
	if err != nil {
		return nil, ErrHelperUnavailable
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	id := c.reqID.Add(1)
	paramBytes, _ := json.Marshal(params)
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  json.RawMessage(paramBytes),
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("helperclient: marshal: %w", err)
	}
	if err := helper.WriteMessage(conn, payload); err != nil {
		return nil, err
	}

	respBytes, err := helper.ReadMessage(conn)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("helperclient: unmarshal: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("helper error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// Health returns the helper's health status.
func (c *Client) Health() (map[string]any, error) {
	raw, err := c.call("helper.health", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}

// PowermetricsSample requests a one-shot powermetrics sample.
// durationMS is the sample window in milliseconds (0 → default 500ms).
func (c *Client) PowermetricsSample(durationMS int) (map[string]any, error) {
	raw, err := c.call("helper.powermetrics.sample", map[string]any{"duration_ms": durationMS})
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}

// TCCSystemQuery returns the TCC grants for bundleID from the system TCC.db.
func (c *Client) TCCSystemQuery(bundleID string) (map[string]any, error) {
	raw, err := c.call("helper.tcc.system.query", map[string]any{"bundle_id": bundleID})
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}

// ProcessTree returns the full process tree (including system daemons).
func (c *Client) ProcessTree() (map[string]any, error) {
	raw, err := c.call("helper.process.tree", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}
