package hostwatch

import (
	"testing"
	"time"
)

func TestLoadConfigJVMHistory(t *testing.T) {
	if d := DefaultConfig().JVMHistory; d.Enabled || d.Interval != 5*time.Minute || d.Timeout != 30*time.Second {
		t.Fatalf("default jvm_history must be disabled at 5m/30s: %+v", d)
	}
	ok := func(string) ([]byte, error) {
		return []byte("jvm_history:\n  enabled: true\n  interval: 10m\n  timeout: 1m\n"), nil
	}
	c, err := LoadConfig("x", ok)
	if err != nil || !c.JVMHistory.Enabled || c.JVMHistory.Interval != 10*time.Minute || c.JVMHistory.Timeout != time.Minute {
		t.Fatalf("jvm_history config: %+v %v", c.JVMHistory, err)
	}
	for name, yml := range map[string]string{
		"interval too short": "interval: 10s",
		"interval too long":  "interval: 3h",
		"timeout too short":  "timeout: 100ms",
		"timeout over cycle": "interval: 2m\n  timeout: 5m",
	} {
		bad := func(string) ([]byte, error) { return []byte("jvm_history:\n  enabled: true\n  " + yml + "\n"), nil }
		c, err = LoadConfig("x", bad)
		j := c.JVMHistory
		if err == nil || !j.Enabled || j.Interval < minJVMHistoryInterval || j.Interval > maxJVMHistoryInterval || j.Timeout < minJVMHistoryTimeout || j.Timeout > j.Interval {
			t.Fatalf("%s: not clamped: %+v %v", name, j, err)
		}
	}
}
