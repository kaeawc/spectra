package hostwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/store"
)

type Sampler interface {
	Collect(context.Context) (Sample, error)
}
type SampleStore interface {
	ListAlerts(context.Context, string, int) ([]store.AlertRow, error)
	SaveHostSample(context.Context, time.Time, []byte) error
	UpsertAlert(context.Context, store.AlertRow) error
	SaveProcessMetrics(context.Context, []store.ProcessMetricRow) error
	PruneHostSamples(context.Context, int) (int64, error)
	PruneResolvedAlerts(context.Context, int) (int64, error)
	PruneJVMSamples(context.Context, int) (int64, error)
	PruneFDSamples(context.Context, int) (int64, error)
	ListHostSamples(context.Context, time.Time, int) ([]store.HostSampleRow, error)
	GetProcessMetrics(context.Context, int, int) ([]store.ProcessMetricRow, error)
	AckAlert(context.Context, string, time.Time) (store.AlertRow, error)
}
type ServiceOptions struct {
	Collector Sampler
	Store     SampleStore
	Metrics   *metrics.Collector
	Config    Config
	ConfigSet bool
	Interval  time.Duration
	Clock     clock.Clock
	IDs       idgen.Generator
	Logger    logger.Logger
	Notifier  Notifier
}
type subscriber struct {
	ch      chan notification
	samples bool
}
type notification struct {
	method string
	value  any
}
type Service struct {
	opts                 ServiceOptions
	engine               *Engine
	mu                   sync.RWMutex
	sampleMu             sync.Mutex
	window               []Sample
	current              Sample
	subscribers          map[*subscriber]struct{}
	interval             time.Duration
	cpuHistory           []float64
	lastFlush, lastPrune time.Time
}

func NewService(opts ServiceOptions) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Logger == nil {
		opts.Logger = logger.Discard()
	}
	if opts.Interval <= 0 {
		opts.Interval = 15 * time.Second
	}
	if !opts.ConfigSet && !opts.Config.set {
		opts.Config = DefaultConfig()
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.NewCollector()
	}
	if opts.Collector == nil {
		opts.Collector = &Collector{Clock: opts.Clock, Metrics: opts.Metrics, Logger: opts.Logger}
	}
	if opts.Notifier == nil {
		opts.Notifier = NoopNotifier{}
	}
	return &Service{opts: opts, engine: NewEngine(opts.IDs), subscribers: map[*subscriber]struct{}{}, interval: opts.Interval}
}
func (s *Service) Run(ctx context.Context) error {
	if s.opts.Store == nil {
		return fmt.Errorf("watch store is required")
	}
	rows, err := s.opts.Store.ListAlerts(ctx, "firing", 2000)
	if err != nil {
		return fmt.Errorf("watch restore alerts: %w", err)
	}
	var restored []Alert
	for _, r := range rows {
		restored = append(restored, fromRow(r))
	}
	s.mu.Lock()
	s.engine.Restore(restored)
	s.mu.Unlock()
	var inFlight <-chan error
	timer := time.NewTimer(s.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.waitForTick(inFlight)
			return nil
		case <-timer.C:
			if inFlight != nil {
				select {
				case <-inFlight:
					inFlight = nil
				default:
				}
			}
			if ctx.Err() != nil {
				s.waitForTick(inFlight)
				return nil
			}
			if inFlight == nil {
				tickCtx, cancel := context.WithTimeout(ctx, s.interval)
				done := make(chan error, 1)
				inFlight = done
				go func() { done <- s.Tick(tickCtx) }()
				select {
				case err := <-done:
					inFlight = nil
					if err != nil {
						s.opts.Logger.Warn("watch tick failed", "error", err)
					}
				case <-tickCtx.Done():
					if ctx.Err() == nil {
						s.opts.Logger.Warn("watch tick timed out")
					}
				}
				cancel()
			} else {
				s.opts.Logger.Warn("watch tick skipped: collector still busy")
			}
			s.mu.RLock()
			interval := s.interval
			s.mu.RUnlock()
			timer.Reset(interval)
		}
	}
}
func (s *Service) waitForTick(done <-chan error) {
	if done == nil {
		return
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.opts.Logger.Warn("watch tick still running after shutdown wait")
	}
}
func (s *Service) Tick(ctx context.Context) error {
	s.sampleMu.Lock()
	sample, err := s.opts.Collector.Collect(ctx)
	s.sampleMu.Unlock()
	if err != nil {
		return fmt.Errorf("watch collect: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.current = sample
	s.window = append(s.window, sample)
	cutoff := sample.At.Add(-2 * time.Hour)
	first := 0
	for first < len(s.window) && s.window[first].At.Before(cutoff) {
		first++
	}
	s.window = append([]Sample(nil), s.window[first:]...)
	conditions := Evaluate(s.opts.Config, s.window)
	events := s.engine.Evaluate(sample.At, conditions)
	s.updateSelfGuard(sample.SelfCPUPct)
	s.mu.Unlock()
	data, err := json.Marshal(sample)
	if err != nil {
		return fmt.Errorf("watch encode sample: %w", err)
	}
	if err = s.opts.Store.SaveHostSample(ctx, sample.At, data); err != nil {
		s.opts.Logger.Error("watch sample persistence failed", "error", err)
	}
	for _, event := range events {
		if err = s.opts.Store.UpsertAlert(ctx, toRow(event.Alert)); err != nil {
			s.opts.Logger.Error("watch alert persistence failed", "error", err)
		}
	}
	s.mu.Lock()
	s.fanoutLocked(notification{"watch.sample", sample}, true)
	for _, event := range events {
		s.fanoutLocked(notification{"alerts.event", event}, false)
	}
	s.mu.Unlock()
	for _, event := range events {
		if event.Type != "resolved" {
			if err = s.opts.Notifier.Notify(ctx, event.Alert); err != nil {
				s.opts.Logger.Warn("watch desktop notification failed", "error", err)
			}
		}
	}
	s.maintenance(ctx, sample.At)
	return nil
}
func (s *Service) updateSelfGuard(cpu float64) {
	s.cpuHistory = append(s.cpuHistory, cpu)
	if len(s.cpuHistory) > 10 {
		s.cpuHistory = s.cpuHistory[1:]
	}
	if len(s.cpuHistory) < 10 {
		return
	}
	var sum float64
	for _, v := range s.cpuHistory {
		sum += v
	}
	avg := sum / float64(len(s.cpuHistory))
	old := s.interval
	if avg > 2 && s.interval < 4*s.opts.Interval {
		s.interval *= 2
		if s.interval > 4*s.opts.Interval {
			s.interval = 4 * s.opts.Interval
		}
	} else if avg < 0.5 && s.interval > s.opts.Interval {
		s.interval = s.opts.Interval
	}
	if old != s.interval {
		s.opts.Logger.Warn("watch interval adjusted for daemon CPU", "cpu_pct", avg, "interval", s.interval)
	}
}
func (s *Service) maintenance(ctx context.Context, at time.Time) {
	if s.lastFlush.IsZero() {
		s.lastFlush = at
		s.lastPrune = at
		return
	}
	if at.Sub(s.lastFlush) >= time.Minute {
		aggs := s.opts.Metrics.FlushAggregates(metrics.DefaultRetainWindow)
		rows := make([]store.ProcessMetricRow, 0, len(aggs))
		for _, a := range aggs {
			rows = append(rows, store.ProcessMetricRow{PID: a.PID, MinuteAt: a.MinuteAt, AvgRSSKiB: a.AvgRSSKiB, MaxRSSKiB: a.MaxRSSKiB, AvgCPUPct: a.AvgCPUPct, MaxCPUPct: a.MaxCPUPct, SampleCount: a.SampleCount})
		}
		if err := s.opts.Store.SaveProcessMetrics(ctx, rows); err != nil {
			s.opts.Logger.Warn("watch process metrics flush failed", "error", err)
		}
		s.lastFlush = at
	}
	if at.Sub(s.lastPrune) < time.Hour {
		return
	}
	for _, op := range []struct {
		fn   func(context.Context, int) (int64, error)
		days int
	}{{s.opts.Store.PruneHostSamples, 7}, {s.opts.Store.PruneResolvedAlerts, 30}, {s.opts.Store.PruneJVMSamples, 7}, {s.opts.Store.PruneFDSamples, 7}} {
		if _, err := op.fn(ctx, op.days); err != nil {
			s.opts.Logger.Warn("watch prune failed", "error", err)
		}
	}
	s.lastPrune = at
}
func (s *Service) fanoutLocked(n notification, samples bool) {
	for sub := range s.subscribers {
		if samples && !sub.samples {
			continue
		}
		select {
		case sub.ch <- n:
		default:
			s.opts.Logger.Warn("watch subscriber dropped message", "method", n.method)
		}
	}
}
func (s *Service) subscribe(done <-chan struct{}, notify func(string, any) error, samples bool) {
	sub := &subscriber{ch: make(chan notification, 16), samples: samples}
	s.mu.Lock()
	s.subscribers[sub] = struct{}{}
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.subscribers, sub); s.mu.Unlock() }()
		for {
			select {
			case <-done:
				return
			case n := <-sub.ch:
				if err := notify(n.method, n.value); err != nil {
					s.opts.Logger.Warn("watch subscriber notify failed", "error", err)
					return
				}
			}
		}
	}()
}
func (s *Service) Current() (Sample, []Alert) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.engine.Active()
}
func (s *Service) Samples(ctx context.Context, since time.Time, limit int) ([]Sample, error) {
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	s.mu.RLock()
	w := append([]Sample(nil), s.window...)
	s.mu.RUnlock()
	if len(w) > 0 && ((since.IsZero() && len(w) >= limit) || (!since.IsZero() && !since.Before(w[0].At))) {
		var out []Sample
		for i := len(w) - 1; i >= 0 && len(out) < limit; i-- {
			if !w[i].At.Before(since) {
				out = append(out, w[i])
			}
		}
		return out, nil
	}
	rows, err := s.opts.Store.ListHostSamples(ctx, since, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(rows))
	for _, r := range rows {
		var v Sample
		if err = json.Unmarshal(r.JSON, &v); err != nil {
			return nil, fmt.Errorf("watch decode sample: %w", err)
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) Alerts(ctx context.Context, state string, limit int) ([]Alert, error) {
	rows, err := s.opts.Store.ListAlerts(ctx, state, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Alert, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}
func (s *Service) Ack(ctx context.Context, id string) (Alert, error) {
	at := s.opts.Clock.Now().UTC()
	r, err := s.opts.Store.AckAlert(ctx, id, at)
	if err != nil {
		return Alert{}, err
	}
	s.mu.Lock()
	s.engine.Ack(id, at)
	s.mu.Unlock()
	return fromRow(r), nil
}
func (s *Service) ProcessHistory(ctx context.Context, pid, limit int) ([]metrics.Sample, error) {
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	s.sampleMu.Lock()
	v := s.opts.Metrics.Recent(pid, limit)
	s.sampleMu.Unlock()
	if len(v) > 0 {
		sort.Slice(v, func(i, j int) bool { return v[i].TakenAt.After(v[j].TakenAt) })
		return v, nil
	}
	rows, err := s.opts.Store.GetProcessMetrics(ctx, pid, limit)
	if err != nil {
		return nil, err
	}
	out := make([]metrics.Sample, 0, len(rows))
	for _, r := range rows {
		out = append(out, metrics.Sample{PID: r.PID, TakenAt: r.MinuteAt, RSSKiB: r.AvgRSSKiB, CPUPct: r.AvgCPUPct})
	}
	return out, nil
}
func toRow(a Alert) store.AlertRow {
	return store.AlertRow{ID: a.ID, Key: a.Key, Severity: a.Severity, Title: a.Title, Detail: a.Detail, State: a.State, FiredAt: a.FiredAt, ResolvedAt: a.ResolvedAt, AckedAt: a.AckedAt}
}
func fromRow(r store.AlertRow) Alert {
	return Alert{ID: r.ID, Key: r.Key, Severity: r.Severity, Title: r.Title, Detail: r.Detail, State: r.State, FiredAt: r.FiredAt, ResolvedAt: r.ResolvedAt, AckedAt: r.AckedAt}
}
