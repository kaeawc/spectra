package hostwatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/store"
)

func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register("watch.current", svc.currentRPC)
	server.Register("watch.samples", svc.samplesRPC)
	server.Register("alerts.list", svc.alertsRPC)
	server.Register("alerts.ack", svc.ackRPC)
	server.Register("alerts.subscribe", svc.subscribeRPC)
	server.Register("process.history", svc.historyRPC)
}
func (s *Service) currentRPC(_ context.Context, _ *daemon.Request) (any, error) {
	sample, alerts := s.Current()
	return struct {
		Sample       Sample      `json:"sample"`
		ActiveAlerts []Alert     `json:"active_alerts"`
		Spawn        *SpawnState `json:"spawn,omitempty"`
	}{sample, alerts, sample.Spawn}, nil
}
func (s *Service) samplesRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		Since string `json:"since"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.Limit < 0 || p.Limit > 2000 {
		return nil, daemon.InvalidParams("limit must be 1..2000")
	}
	var since time.Time
	if p.Since != "" {
		var err error
		since, err = time.Parse(time.RFC3339, p.Since)
		if err != nil {
			return nil, daemon.InvalidParams("since must be RFC3339")
		}
	}
	return s.Samples(ctx, since, p.Limit)
}
func (s *Service) alertsRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		State string `json:"state"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.State == "" {
		p.State = "firing"
	}
	if p.State != "all" && p.State != "firing" && p.State != "resolved" {
		return nil, daemon.InvalidParams("state must be firing, resolved, or all")
	}
	if p.Limit < 0 || p.Limit > 2000 {
		return nil, daemon.InvalidParams("limit must be 1..2000")
	}
	return s.Alerts(ctx, p.State, p.Limit)
}
func (s *Service) ackRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, daemon.InvalidParams("id is required")
	}
	a, err := s.Ack(ctx, p.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, daemon.InvalidParams("unknown alert id")
	}
	return a, err
}
func (s *Service) subscribeRPC(_ context.Context, req *daemon.Request) (any, error) {
	var p struct {
		Samples bool `json:"samples"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	s.subscribe(req.Done, req.Notify, p.Samples)
	return map[string]bool{"subscribed": true}, nil
}
func (s *Service) historyRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		PID   int `json:"pid"`
		Limit int `json:"limit"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.PID <= 0 || p.Limit < 0 || p.Limit > 2000 {
		return nil, daemon.InvalidParams("positive pid and limit <= 2000 required")
	}
	return s.ProcessHistory(ctx, p.PID, p.Limit)
}
func decodeParams(req *daemon.Request, dst any) error {
	if len(req.Params) == 0 || string(req.Params) == "null" {
		return nil
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		return daemon.InvalidParams("invalid params: %v", err)
	}
	return nil
}
