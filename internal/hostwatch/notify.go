package hostwatch

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

type Notifier interface {
	Notify(context.Context, Alert) error
}
type NoopNotifier struct{}

func (NoopNotifier) Notify(context.Context, Alert) error { return nil }

type DesktopNotifier struct {
	OS       hostos.Kind
	Runner   proc.Runner
	LookPath func(string) (string, error)
}

func (n DesktopNotifier) Notify(ctx context.Context, a Alert) error {
	return notifyDesktop(ctx, hostos.Resolve(n.OS), n.Runner, n.LookPath, a)
}
func appleEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 240 {
			break
		}
		if unicode.IsControl(r) {
			continue
		}
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

type RateLimitedNotifier struct {
	Inner Notifier
	Clock clock.Clock
	mu    sync.Mutex
	last  map[string]time.Time
	sent  []time.Time
}

func (n *RateLimitedNotifier) Notify(ctx context.Context, a Alert) error {
	if n.Inner == nil {
		return nil
	}
	clk := n.Clock
	if clk == nil {
		clk = clock.System{}
	}
	at := clk.Now()
	n.mu.Lock()
	if n.last == nil {
		n.last = map[string]time.Time{}
	}
	if prev, ok := n.last[a.Key]; ok && at.Sub(prev) < 15*time.Minute && a.Severity != "critical" {
		n.mu.Unlock()
		return nil
	}
	recent := n.sent[:0]
	for _, v := range n.sent {
		if at.Sub(v) < time.Hour {
			recent = append(recent, v)
		}
	}
	n.sent = recent
	if len(n.sent) >= 6 {
		n.mu.Unlock()
		return nil
	}
	n.last[a.Key] = at
	n.sent = append(n.sent, at)
	n.mu.Unlock()
	return n.Inner.Notify(ctx, a)
}
