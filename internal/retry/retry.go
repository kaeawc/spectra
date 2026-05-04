// Package retry runs an operation with bounded retries and configurable
// backoff. The Sleeper dependency lets tests skip real time waits.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/random"
)

// Sleeper sleeps for d or until ctx is canceled, returning ctx.Err in the
// latter case.
type Sleeper interface {
	Sleep(ctx context.Context, d time.Duration) error
}

// Real is a Sleeper backed by time.After.
type Real struct{}

// Sleep returns ctx.Err on cancellation, nil otherwise.
func (Real) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Instant is a Sleeper that records every requested delay and returns
// immediately. Use in tests to assert backoff behavior without waiting.
type Instant struct {
	Delays []time.Duration
}

// Sleep records d and returns ctx.Err if already canceled, else nil.
func (i *Instant) Sleep(ctx context.Context, d time.Duration) error {
	i.Delays = append(i.Delays, d)
	return ctx.Err()
}

// Policy describes retry behavior. Zero-value defaults: 3 attempts, 100ms
// base delay, exponential backoff capped at 5s, no jitter, retry every error.
type Policy struct {
	// MaxAttempts is the total number of attempts including the first.
	// Zero means 3.
	MaxAttempts int
	// BaseDelay is the initial delay before the second attempt. Zero means
	// 100ms.
	BaseDelay time.Duration
	// MaxDelay caps the exponential growth. Zero means 5s.
	MaxDelay time.Duration
	// Multiplier scales the delay between attempts. Zero means 2.0.
	Multiplier float64
	// JitterFraction perturbs each delay by ±fraction (e.g. 0.2 = ±20%).
	// Zero disables jitter. Requires the Executor to have a Random source.
	JitterFraction float64
	// Retryable returns true if err warrants another attempt. Nil means
	// retry on every non-nil error.
	Retryable func(err error) bool
	// OnRetry, if non-nil, is invoked before sleeping between attempts.
	OnRetry func(attempt int, err error, delay time.Duration)
}

func (p Policy) maxAttempts() int {
	if p.MaxAttempts <= 0 {
		return 3
	}
	return p.MaxAttempts
}

func (p Policy) baseDelay() time.Duration {
	if p.BaseDelay <= 0 {
		return 100 * time.Millisecond
	}
	return p.BaseDelay
}

func (p Policy) maxDelay() time.Duration {
	if p.MaxDelay <= 0 {
		return 5 * time.Second
	}
	return p.MaxDelay
}

func (p Policy) multiplier() float64 {
	if p.Multiplier <= 0 {
		return 2.0
	}
	return p.Multiplier
}

// Executor runs operations under a Policy. Construct via New.
type Executor struct {
	sleeper Sleeper
	random  random.Random
}

// New returns an Executor. Pass Real{} and random.NewCrypto() in production;
// pass &Instant{} and random.NewSeeded(seed) in tests. The random source may
// be nil if no policy uses JitterFraction.
func New(s Sleeper, r random.Random) *Executor {
	return &Executor{sleeper: s, random: r}
}

// Do executes op until it returns nil or the policy is exhausted. The
// returned error is the last error from op, wrapped with attempt info. If
// ctx is canceled mid-retry, Do returns ctx.Err().
func (e *Executor) Do(ctx context.Context, p Policy, op func(ctx context.Context, attempt int) error) error {
	maxN := p.maxAttempts()
	retryable := p.Retryable
	if retryable == nil {
		retryable = func(error) bool { return true }
	}

	var lastErr error
	for attempt := 1; attempt <= maxN; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := op(ctx, attempt)
		if err == nil {
			return nil
		}
		lastErr = err

		if attempt == maxN {
			break
		}
		if !retryable(err) {
			return err
		}

		delay := e.computeDelay(p, attempt)
		if p.OnRetry != nil {
			p.OnRetry(attempt, err, delay)
		}

		if err := e.sleeper.Sleep(ctx, delay); err != nil {
			return err
		}
	}

	if lastErr == nil {
		return errors.New("retry: no attempts ran")
	}
	return fmt.Errorf("retry: exhausted %d attempts: %w", maxN, lastErr)
}

func (e *Executor) computeDelay(p Policy, attempt int) time.Duration {
	base := float64(p.baseDelay())
	for i := 1; i < attempt; i++ {
		base *= p.multiplier()
		if base >= float64(p.maxDelay()) {
			base = float64(p.maxDelay())
			break
		}
	}
	if p.JitterFraction > 0 && e.random != nil {
		j := p.JitterFraction
		offset := (e.random.Float64()*2 - 1) * j * base
		base += offset
		if base < 0 {
			base = 0
		}
	}
	return time.Duration(base)
}
