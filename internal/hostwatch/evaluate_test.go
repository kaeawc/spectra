package hostwatch

import (
	"testing"
	"time"
)

func baseSample(at time.Time) Sample {
	return Sample{At: at, NCPU: 4, Load1: 1, MemoryPressure: "normal", DataFreeGB: 100, Kinds: map[string]KindStat{}, Limits: map[string]LimitUsage{}}
}
func hasCondition(cs []Condition, key, severity string) bool {
	for _, c := range cs {
		if c.Key == key && c.Severity == severity {
			return true
		}
	}
	return false
}
func TestEvaluateRules(t *testing.T) {
	cfg := DefaultConfig()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	makeWindow := func(n int, change func(*Sample, int)) []Sample {
		w := make([]Sample, n)
		for i := range w {
			w[i] = baseSample(at.Add(time.Duration(i) * time.Minute))
			change(&w[i], i)
		}
		return w
	}
	for _, tt := range []struct {
		name     string
		window   []Sample
		key, sev string
		want     bool
	}{
		{"load needs two", makeWindow(1, func(s *Sample, _ int) { s.Load1 = 13 }), "load.high", "critical", false},
		{"load critical", makeWindow(2, func(s *Sample, _ int) { s.Load1 = 13 }), "load.high", "critical", true},
		{"load climbing", makeWindow(4, func(s *Sample, i int) { s.Load1 = float64(2 + i*3) }), "load.climbing", "warn", true},
		{"memory warn sustain", makeWindow(2, func(s *Sample, _ int) { s.MemoryPressure = "warn" }), "memory.pressure", "warn", true},
		{"memory critical immediate", makeWindow(1, func(s *Sample, _ int) { s.MemoryPressure = "critical" }), "memory.pressure", "critical", true},
		{"swap growth", makeWindow(2, func(s *Sample, i int) { s.SwapUsedMB = float64(i * 1100) }), "memory.swap_growth", "warn", true},
		{"limits", makeWindow(1, func(s *Sample, _ int) { s.Limits["files"] = LimitUsage{1, 1, 91} }), "limit:files", "critical", true},
		{"disk", makeWindow(1, func(s *Sample, _ int) { s.DataFreeGB = 4 }), "disk.free", "critical", true},
		{"thermal", makeWindow(2, func(s *Sample, _ int) { s.ThermalThrottled = true }), "thermal.throttled", "warn", true},
		{"kind cpu", makeWindow(3, func(s *Sample, _ int) { s.Kinds["gradle"] = KindStat{1, 650, 100} }), "kind.cpu:gradle", "warn", true},
		{"kind count", makeWindow(2, func(s *Sample, _ int) { s.Kinds["codex"] = KindStat{4, 0, 0}; s.Kinds["claude"] = KindStat{4, 0, 0} }), "kind.count:agents", "warn", true},
		{"clear", makeWindow(3, func(s *Sample, i int) {
			if i < 2 {
				s.Load1 = 13
			}
		}), "load.high", "critical", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCondition(Evaluate(cfg, tt.window), tt.key, tt.sev)
			if got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
