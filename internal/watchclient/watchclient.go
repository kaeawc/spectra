// Package watchclient is the shared typed daemon client for the CLI, MCP, and editor integrations (LSP).
package watchclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/metrics"
)

type Client struct{ c *daemonclient.Client }

// Disabled reports whether daemon access is disabled by the supplied environment lookup.
func Disabled(env func(string) string) bool {
	return env != nil && (env("SPECTRA_NO_DAEMON") == "1" || strings.EqualFold(env("SPECTRA_NO_DAEMON"), "true"))
}

func Connect(ctx context.Context, env func(string) string, paths daemon.Paths) (*Client, bool) {
	if Disabled(env) {
		return nil, false
	}
	if paths.Socket == "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	c, err := daemonclient.Dial(ctx, paths.Socket)
	if err != nil {
		return nil, false
	}
	return &Client{c: c}, true
}

func (w *Client) CheckVersion(ctx context.Context, version string) error {
	st, err := w.c.Status(ctx)
	if err != nil {
		return fmt.Errorf("daemon status: %w", err)
	}
	return daemonclient.CheckVersion(st, version)
}

func (w *Client) Current(ctx context.Context) (hostwatch.Sample, []hostwatch.Alert, error) {
	var result struct {
		Sample       hostwatch.Sample      `json:"sample"`
		ActiveAlerts []hostwatch.Alert     `json:"active_alerts"`
		Spawn        *hostwatch.SpawnState `json:"spawn"`
	}
	err := w.c.Call(ctx, "watch.current", nil, &result)
	if result.Spawn != nil {
		result.Sample.Spawn = result.Spawn
	}
	return result.Sample, result.ActiveAlerts, err
}

func (w *Client) Samples(ctx context.Context, since time.Time, limit int) ([]hostwatch.Sample, error) {
	var result []hostwatch.Sample
	start := ""
	if !since.IsZero() {
		start = since.UTC().Format(time.RFC3339)
	}
	err := w.c.Call(ctx, "watch.samples", map[string]any{"since": start, "limit": limit}, &result)
	return result, err
}

func (w *Client) Alerts(ctx context.Context, state string, limit int) ([]hostwatch.Alert, error) {
	var result []hostwatch.Alert
	err := w.c.Call(ctx, "alerts.list", map[string]any{"state": state, "limit": limit}, &result)
	return result, err
}

func (w *Client) Ack(ctx context.Context, id string) (hostwatch.Alert, error) {
	var result hostwatch.Alert
	err := w.c.Call(ctx, "alerts.ack", map[string]string{"id": id}, &result)
	return result, err
}

func (w *Client) ProcessHistory(ctx context.Context, pid, limit int) ([]metrics.Sample, error) {
	var result []metrics.Sample
	err := w.c.Call(ctx, "process.history", map[string]int{"pid": pid, "limit": limit}, &result)
	return result, err
}

func (w *Client) Subscribe(ctx context.Context, samples bool, onEvent func(hostwatch.Event), onSample func(hostwatch.Sample)) error {
	type notification struct {
		method string
		data   json.RawMessage
	}
	updates := make(chan notification, 32)
	w.c.OnNotification(func(method string, data json.RawMessage) {
		select {
		case updates <- notification{method, data}:
		case <-ctx.Done():
		}
	})
	defer w.c.OnNotification(nil)
	var subscribed map[string]bool
	if err := w.c.Call(ctx, "alerts.subscribe", map[string]bool{"samples": samples}, &subscribed); err != nil {
		return fmt.Errorf("subscribe alerts: %w", err)
	}
	if !subscribed["subscribed"] {
		return fmt.Errorf("subscribe alerts: daemon did not confirm subscription")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case update := <-updates:
			if err := deliverNotification(update.method, update.data, onEvent, onSample); err != nil {
				return err
			}
		case <-ticker.C:
			probe, cancel := context.WithTimeout(ctx, time.Second)
			_, err := w.c.Status(probe)
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return fmt.Errorf("subscription connection: %w", err)
			}
		}
	}
}

func deliverNotification(method string, data json.RawMessage, onEvent func(hostwatch.Event), onSample func(hostwatch.Sample)) error {
	switch method {
	case "alerts.event":
		var event hostwatch.Event
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode alert event: %w", err)
		}
		if onEvent != nil {
			onEvent(event)
		}
	case "watch.sample":
		var sample hostwatch.Sample
		if err := json.Unmarshal(data, &sample); err != nil {
			return fmt.Errorf("decode watch sample: %w", err)
		}
		if onSample != nil {
			onSample(sample)
		}
	}
	return nil
}

func (w *Client) Close() error { return w.c.Close() }
