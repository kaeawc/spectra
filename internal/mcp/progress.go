package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// progressMinGap rate-limits notifications to at most four a second.
	progressMinGap = 250 * time.Millisecond
	// defaultProgressEvery is the period of time-based progress.
	defaultProgressEvery = 300 * time.Millisecond
)

// progressReporter sends notifications/progress for one call that carried a
// progressToken. A nil reporter (no token) does nothing.
type progressReporter struct {
	send  func(context.Context, *sdk.ProgressNotificationParams) error
	token any
	now   func() time.Time
	every time.Duration

	mu       sync.Mutex
	last     time.Time
	progress float64
	steps    int
}

type progressKey struct{}

func withProgress(ctx context.Context, p *progressReporter) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, p)
}

// progressFrom returns the call's reporter, or nil when the client asked for
// no progress.
func progressFrom(ctx context.Context) *progressReporter {
	p, _ := ctx.Value(progressKey{}).(*progressReporter)
	return p
}

func (s *Server) newProgress(req *sdk.CallToolRequest) *progressReporter {
	if req == nil || req.Params == nil || req.Session == nil {
		return nil
	}
	token := req.Params.GetProgressToken()
	if token == nil {
		return nil
	}
	every := s.progressEvery
	if every <= 0 {
		every = defaultProgressEvery
	}
	return &progressReporter{send: req.Session.NotifyProgress, token: token, now: s.progressClock, every: every}
}

// progressClock times progress. It is wall time even when tests fix the result
// clock, so rate limiting and elapsed reports see time pass.
func (s *Server) progressClock() time.Time { return s.wall().Now() }

// report sends progress (out of total, 0 when unknown) unless the last
// notification was under progressMinGap ago or progress has not advanced.
func (p *progressReporter) report(ctx context.Context, progress, total float64, msg string) {
	if p == nil || ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	now := p.now()
	if progress <= p.progress || (!p.last.IsZero() && now.Sub(p.last) < progressMinGap) {
		p.mu.Unlock()
		return
	}
	p.last, p.progress = now, progress
	p.mu.Unlock()
	_ = p.send(ctx, &sdk.ProgressNotificationParams{ProgressToken: p.token, Progress: progress, Total: total, Message: msg})
}

// step reports the start of the call's next phase; the total is unknown
// because phases depend on the arguments.
func (p *progressReporter) step(ctx context.Context, name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.steps++
	n := p.steps
	p.mu.Unlock()
	p.report(ctx, float64(n), 0, name)
}

// elapsed reports wall-clock progress through a call of known length until
// the returned stop function runs. label names what is being waited on.
func (p *progressReporter) elapsed(ctx context.Context, total time.Duration, label string) (stop func()) {
	if p == nil || total <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		start := p.now()
		ticker := time.NewTicker(p.every)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				e := p.now().Sub(start)
				if e > total {
					e = total
				}
				p.report(ctx, e.Seconds(), total.Seconds(), fmt.Sprintf("%s: %s of %s", label, e.Round(100*time.Millisecond), total))
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}
