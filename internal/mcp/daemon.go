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
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var errDaemonUnavailable = errors.New("spectra daemon is not running")
var errDaemonTimeout = errors.New("spectra daemon call timed out")

const defaultDaemonCallTimeout = 2 * time.Second

func (s *Server) setDaemonCallTimeout(timeout time.Duration) { s.daemonTimeout = timeout }

func (s *Server) daemonCallTimeout() time.Duration {
	if s.daemonTimeout > 0 {
		return s.daemonTimeout
	}
	return defaultDaemonCallTimeout
}

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

// The lazily connected daemon client is not safe for concurrent use; callers
// hold the call slot (s.acquire), which serializes every tool call and
// resource read.
func (s *Server) withDaemon(parentCtx context.Context, fn func(context.Context, daemonWatchClient) error) error {
	ctx, cancel := context.WithTimeout(parentCtx, s.daemonCallTimeout())
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
		if errors.Is(err, context.DeadlineExceeded) && parentCtx.Err() == nil {
			_ = s.daemon.Close()
			s.daemon = nil
			return errDaemonTimeout
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
	if errors.Is(err, errDaemonTimeout) {
		return toolTextError(map[string]string{"error": "daemon_timeout", "message": "the spectra daemon did not respond within 2s; check `spectra daemon status` / `spectra daemon logs`"})
	}
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

func (s *Server) toolHostHealth(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Samples int `json:"samples"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	if p.Samples < 0 || p.Samples > 240 {
		return toolError("samples must be 0..240")
	}
	var sample hostwatch.Sample
	var alerts []hostwatch.Alert
	var recent []hostwatch.Sample
	err := s.withDaemon(ctx, func(ctx context.Context, client daemonWatchClient) error {
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
	if sample.At.IsZero() {
		return toolText(toolEnvelope{
			Summary:     fmt.Sprintf("the daemon has not taken its first host sample yet; %d active alerts", len(alerts)),
			NextActions: []string{"Retry host_health in a few seconds, or use load for a direct reading."},
			Raw:         map[string]any{"sample": nil, "active_alerts": alerts, "recent_samples": recent},
			Timestamp:   s.now(),
		})
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("host load %.2f across %d cores; memory pressure %s; %d active alerts", sample.Load1, sample.NCPU, sample.MemoryPressure, len(alerts)), Raw: map[string]any{"sample": sample, "active_alerts": alerts, "recent_samples": recent}, Timestamp: s.now()})
}

func (s *Server) toolAlerts(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Action string `json:"action"`
		State  string `json:"state"`
		Limit  int    `json:"limit"`
		ID     string `json:"id"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
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
	err := s.withDaemon(ctx, func(ctx context.Context, client daemonWatchClient) error {
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

func (s *Server) toolProcessHistory(ctx context.Context, pid, limit int) ToolResult {
	if pid <= 0 {
		return toolError("process history requires pid")
	}
	var history any
	err := s.withDaemon(ctx, func(ctx context.Context, client daemonWatchClient) error {
		rows, callErr := client.ProcessHistory(ctx, pid, limit)
		history = rows
		return callErr
	})
	if err != nil {
		if errors.Is(err, errDaemonTimeout) {
			return daemonToolError(err)
		}
		if !errors.Is(err, errDaemonUnavailable) {
			return toolErrorFrom(err)
		}
		return toolError("process history is unavailable in the local-only MCP server; start it with `spectra daemon start` or install it with `spectra daemon install`")
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("process history for pid %d", pid), Raw: history, Timestamp: s.now()})
}

func (s *Server) readFiringAlerts(ctx context.Context, _ *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var alerts []hostwatch.Alert
	err = s.withDaemon(ctx, func(ctx context.Context, client daemonWatchClient) error {
		var callErr error
		alerts, callErr = client.Alerts(ctx, "firing", 2000)
		return callErr
	})
	var value any = alerts
	if reason := s.alertsUnavailableReason(); reason != "" && err == nil {
		value = map[string]any{"daemon": "alerts unavailable", "message": "the daemon accepted the connection but its alert stream failed: " + reason, "alerts": []hostwatch.Alert{}}
	}
	switch {
	case errors.Is(err, errDaemonUnavailable):
		// A subscriber keeps reading this resource while the daemon is down;
		// say so in the content and send resources/updated when it returns.
		value = map[string]any{"daemon": "not running", "message": "start it with `spectra daemon start` or install it with `spectra daemon install`", "alerts": []hostwatch.Alert{}}
	case err != nil:
		return nil, &jsonrpc.Error{Code: -32000, Message: fmt.Sprintf("%v; start it with `spectra daemon start`", err)}
	}
	return jsonResource(firingAlertsURI, value, alertsTTLMs)
}

// Cache hints for the live resources. Both describe this host, so they are
// never shared; a subscriber is told about changes, so a short TTL is safe.
const (
	alertsTTLMs     = 5_000
	hostHealthTTLMs = 10_000
)

func jsonResource(uri string, value any, ttlMs int) (*sdk.ReadResourceResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	res := &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(data)}}}
	res.CacheScope = "private"
	res.TTLMs = ttlMs
	return res, nil
}

// hostHealthDoc is the compact host-health resource: the fields its
// notifications track plus the numbers behind them.
type hostHealthDoc struct {
	Daemon           string    `json:"daemon"`
	At               time.Time `json:"at"`
	NCPU             int       `json:"ncpu"`
	Load1            float64   `json:"load1"`
	Load5            float64   `json:"load5"`
	Load15           float64   `json:"load15"`
	LoadPerCPU       float64   `json:"load_per_cpu"`
	LoadVerdict      string    `json:"load_verdict"`
	MemoryPressure   string    `json:"memory_pressure"`
	MemFreePct       float64   `json:"mem_free_pct"`
	SwapUsedMB       float64   `json:"swap_used_mb"`
	DataFreeGB       float64   `json:"data_free_gb"`
	ThermalThrottled bool      `json:"thermal_throttled"`
	ActiveAlerts     *int      `json:"active_alerts,omitempty"`
}

func newHostHealthDoc(s hostwatch.Sample, daemonUp bool, activeAlerts []hostwatch.Alert, verdict string) hostHealthDoc {
	if verdict == "" {
		verdict = loadVerdict(s.Load1, s.NCPU, "")
	}
	doc := hostHealthDoc{
		Daemon: "not running", At: s.At, NCPU: s.NCPU,
		Load1: round2(s.Load1), Load5: round2(s.Load5), Load15: round2(s.Load15),
		LoadPerCPU: round2(loadPerCPU(s.Load1, s.NCPU)), LoadVerdict: verdict,
		MemoryPressure: s.MemoryPressure, MemFreePct: round2(s.MemFreePct), SwapUsedMB: round2(s.SwapUsedMB),
		DataFreeGB: round2(s.DataFreeGB), ThermalThrottled: s.ThermalThrottled,
	}
	if daemonUp {
		n := len(activeAlerts)
		doc.Daemon, doc.ActiveAlerts = "running", &n
	}
	return doc
}

// readHostHealth serves the daemon's current sample, or samples the host
// locally when the daemon is not running.
func (s *Server) readHostHealth(ctx context.Context, _ *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var sample hostwatch.Sample
	var alerts []hostwatch.Alert
	err = s.withDaemon(ctx, func(ctx context.Context, client daemonWatchClient) error {
		var callErr error
		sample, alerts, callErr = client.Current(ctx)
		return callErr
	})
	switch {
	case err == nil:
		if s.alertsUnavailableReason() != "" {
			// The watcher samples locally while the daemon's stream is
			// unusable; serve that sample so content matches its updates.
			if local, lerr := s.localHostSample(ctx); lerr == nil {
				return jsonResource(hostHealthURI, newHostHealthDoc(local, true, alerts, s.hubVerdict(false)), hostHealthTTLMs)
			}
		}
		return jsonResource(hostHealthURI, newHostHealthDoc(sample, true, alerts, s.hubVerdict(true)), hostHealthTTLMs)
	case !errors.Is(err, errDaemonUnavailable):
		return nil, &jsonrpc.Error{Code: -32000, Message: err.Error()}
	}
	sample, err = s.localHostSample(ctx)
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32000, Message: fmt.Sprintf("sample host: %v", err)}
	}
	return jsonResource(hostHealthURI, newHostHealthDoc(sample, false, nil, s.hubVerdict(false)), hostHealthTTLMs)
}

// hubVerdict is the hysteresis-aware verdict the live hub notifies with, or
// "" when no subscription is tracking it.
func (s *Server) hubVerdict(daemon bool) string {
	if s.live == nil {
		return ""
	}
	v, _ := s.live.levelVerdict(daemon)
	return v
}

func (s *Server) alertsUnavailableReason() string {
	if s.live == nil {
		return ""
	}
	return s.live.alertsUnavailable()
}

func (s *Server) localHostSample(ctx context.Context) (hostwatch.Sample, error) {
	cfg := s.liveCfg.withDefaults()
	if s.live != nil {
		if sample, ok := s.live.cachedLocalSample(); ok {
			return sample, nil
		}
		cfg = s.live.cfg
	}
	ctx, cancel := context.WithTimeout(ctx, localSampleTimeout)
	defer cancel()
	return cfg.sampleLocal(ctx)
}
