package helperclient

import "testing"

func TestPowermetricsParamsOmitsNonPositiveDuration(t *testing.T) {
	for _, ms := range []int{0, -1} {
		if _, ok := powermetricsParams(ms, nil)["duration_ms"]; ok {
			t.Errorf("duration %d should be omitted so the helper applies its default", ms)
		}
	}
	p := powermetricsParams(250, []string{"tasks"})
	if p["duration_ms"] != 250 {
		t.Errorf("duration_ms = %v, want 250", p["duration_ms"])
	}
	if s, ok := p["samplers"].([]string); !ok || len(s) != 1 || s[0] != "tasks" {
		t.Errorf("samplers = %v, want [tasks]", p["samplers"])
	}
	if _, ok := powermetricsParams(250, nil)["samplers"]; ok {
		t.Error("nil samplers should be omitted")
	}
}
