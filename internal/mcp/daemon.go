package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/watchclient"
)

var errDaemonUnavailable = errors.New("spectra daemon is not running")

type daemonWatchClient interface {
	Current(context.Context) (hostwatch.Sample, []hostwatch.Alert, error)
	Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error)
	Alerts(context.Context, string, int) ([]hostwatch.Alert, error)
	Ack(context.Context, string) (hostwatch.Alert, error)
	ProcessHistory(context.Context, int, int) ([]metrics.Sample, error)
	Close() error
}

func defaultDaemonConnect(ctx context.Context) (daemonWatchClient, bool) {
	paths, err := daemon.DefaultPaths(os.Getenv, os.UserHomeDir, runtime.GOOS)
	if err != nil {
		return nil, false
	}
	return watchclient.Connect(ctx, os.Getenv, paths)
}

func (s *Server) SetDaemonConnector(fn func(context.Context) (daemonWatchClient, bool)) {
	if s.daemon != nil {
		_ = s.daemon.Close()
		s.daemon = nil
	}
	s.connectDaemon = fn
}

func (s *Server) withDaemon(fn func(context.Context, daemonWatchClient) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		if s.daemon == nil {
			connect := s.connectDaemon
			if connect == nil {
				connect = defaultDaemonConnect
			}
			client, ok := connect(ctx)
			if !ok {
				return errDaemonUnavailable
			}
			s.daemon = client
		}
		err := fn(ctx, s.daemon)
		if err == nil {
			return nil
		}
		if !daemonclient.IsUnavailable(err) {
			return err
		}
		_ = s.daemon.Close()
		s.daemon = nil
	}
	return errDaemonUnavailable
}

func daemonToolError(err error) ToolResult {
	if !errors.Is(err, errDaemonUnavailable) {
		return toolTextError(map[string]string{"error": "daemon_error", "message": err.Error()})
	}
	return toolTextError(map[string]string{"error": "daemon_unavailable", "message": fmt.Sprintf("%v; start it with `spectra daemon start` or install it with `spectra daemon install`", err)})
}

func toolTextError(value any) ToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return toolError(err.Error())
	}
	return ToolResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: string(data)}}}
}

func (s *Server) toolHostHealth(raw json.RawMessage) ToolResult {
	var p struct {
		Samples int `json:"samples"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	if p.Samples < 0 || p.Samples > 240 {
		return toolError("samples must be 0..240")
	}
	var sample hostwatch.Sample
	var alerts []hostwatch.Alert
	var recent []hostwatch.Sample
	err := s.withDaemon(func(ctx context.Context, client daemonWatchClient) error {
		var callErr error
		sample, alerts, callErr = client.Current(ctx)
		if callErr != nil || p.Samples == 0 {
			return callErr
		}
		recent, callErr = client.Samples(ctx, time.Time{}, p.Samples)
		return callErr
	})
	if err != nil {
		return daemonToolError(err)
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("host load %.2f across %d cores; memory pressure %s; %d active alerts", sample.Load1, sample.NCPU, sample.MemoryPressure, len(alerts)), Raw: map[string]any{"sample": sample, "active_alerts": alerts, "recent_samples": recent}, Timestamp: s.now()})
}

func (s *Server) toolAlerts(raw json.RawMessage) ToolResult {
	var p struct {
		Action string `json:"action"`
		State  string `json:"state"`
		Limit  int    `json:"limit"`
		ID     string `json:"id"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	if p.Action == "" {
		p.Action = "list"
	}
	if p.State == "" {
		p.State = "firing"
	}
	if p.Limit < 0 || p.Limit > 2000 {
		return toolError("limit must be 1..2000")
	}
	if p.Action != "list" && p.Action != "ack" {
		return toolError("action must be list or ack")
	}
	if p.State != "firing" && p.State != "resolved" && p.State != "all" {
		return toolError("state must be firing, resolved, or all")
	}
	if p.Action == "ack" && p.ID == "" {
		return toolError("id is required for ack")
	}
	var value any
	err := s.withDaemon(func(ctx context.Context, client daemonWatchClient) error {
		if p.Action == "ack" {
			a, err := client.Ack(ctx, p.ID)
			value = a
			return err
		}
		alerts, err := client.Alerts(ctx, p.State, p.Limit)
		value = alerts
		return err
	})
	if err != nil {
		return daemonToolError(err)
	}
	return toolText(toolEnvelope{Summary: "daemon alerts " + p.Action, Raw: value, Timestamp: s.now()})
}

func (s *Server) toolProcessHistory(pid, limit int) ToolResult {
	if pid <= 0 {
		return toolError("process history requires pid")
	}
	var history any
	err := s.withDaemon(func(ctx context.Context, client daemonWatchClient) error {
		rows, callErr := client.ProcessHistory(ctx, pid, limit)
		history = rows
		return callErr
	})
	if err != nil {
		if !errors.Is(err, errDaemonUnavailable) {
			return toolError(err.Error())
		}
		return toolError("process history is unavailable in the local-only MCP server; start it with `spectra daemon start` or install it with `spectra daemon install`")
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("process history for pid %d", pid), Raw: history, Timestamp: s.now()})
}

func (s *Server) readFiringAlerts(req Request) {
	var alerts []hostwatch.Alert
	err := s.withDaemon(func(ctx context.Context, client daemonWatchClient) error {
		var callErr error
		alerts, callErr = client.Alerts(ctx, "firing", 2000)
		return callErr
	})
	if err != nil {
		s.sendResponse(req.ID, &RPCError{Code: -32000, Message: fmt.Sprintf("%v; start it with `spectra daemon start`", err)})
		return
	}
	data, err := json.Marshal(alerts)
	if err != nil {
		s.sendResponse(req.ID, &RPCError{Code: -32603, Message: err.Error()})
		return
	}
	s.sendResponse(req.ID, nil, ResourceReadResult{Contents: []ResourceContent{{URI: "spectra://alerts/firing", MimeType: "application/json", Text: string(data)}}})
}
