package hostwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
)

type countNotifier struct{ n int }

func (n *countNotifier) Notify(context.Context, Alert) error { n.n++; return nil }
func TestAppleEscape(t *testing.T) {
	s := `" & do shell script "rm -rf /"` + "\\\n\r"
	got := appleEscape(s)
	if strings.ContainsAny(got, "\n\r") || !strings.Contains(got, `\" & do shell script \"`) {
		t.Fatalf("unsafe: %q", got)
	}
	if len(appleEscape(strings.Repeat("a", 1000))) > 240 {
		t.Fatal("not capped")
	}
}
func TestRateLimit(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	inner := &countNotifier{}
	n := &RateLimitedNotifier{Inner: inner, Clock: clk}
	a := Alert{Key: "a", Severity: "warn"}
	_ = n.Notify(context.Background(), a)
	_ = n.Notify(context.Background(), a)
	if inner.n != 1 {
		t.Fatalf("cooldown: %d", inner.n)
	}
	a.Severity = "critical"
	_ = n.Notify(context.Background(), a)
	if inner.n != 2 {
		t.Fatal("escalation suppressed")
	}
	for i := 0; i < 10; i++ {
		a.Key = string(rune('b' + i))
		_ = n.Notify(context.Background(), a)
	}
	if inner.n != 6 {
		t.Fatalf("global limit: %d", inner.n)
	}
	clk.Advance(time.Hour)
	_ = n.Notify(context.Background(), a)
	if inner.n != 7 {
		t.Fatal("hourly reset")
	}
}
