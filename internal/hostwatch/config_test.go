package hostwatch

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte("load:\n  warn_multiple: 2\n"), nil }
	c, err := LoadConfig("x", read)
	if err != nil || c.Load.WarnMultiple != 2 || c.Load.CriticalMultiple != 3 {
		t.Fatalf("config: %+v %v", c, err)
	}
	c, err = LoadConfig("x", func(string) ([]byte, error) { return []byte("unexpected: true"), nil })
	if err == nil || c.Load.WarnMultiple != 1.5 {
		t.Fatalf("unknown key: %+v %v", c, err)
	}
	_, err = LoadConfig("x", func(string) ([]byte, error) { return nil, errors.New("bad") })
	if err == nil {
		t.Fatal("read error lost")
	}
}

func TestLoadConfigInvalidFieldKeepsOtherSettings(t *testing.T) {
	read := func(string) ([]byte, error) {
		return []byte("load:\n  warn_multiple: 0\nmemory:\n  swap_growth_mb: 256\n"), nil
	}
	c, err := LoadConfig("watch.yml", read)
	if err == nil || c.Load.WarnMultiple != 1.5 || c.Memory.SwapGrowthMB != 256 {
		t.Fatalf("config: %+v error: %v", c, err)
	}
	svc := NewService(ServiceOptions{Config: c})
	if svc.opts.Config.Memory.SwapGrowthMB != 256 {
		t.Fatalf("service lost custom config: %+v", svc.opts.Config)
	}
}

func TestNewServiceDefaultConfig(t *testing.T) {
	svc := NewService(ServiceOptions{})
	if !reflect.DeepEqual(svc.opts.Config, DefaultConfig()) {
		t.Fatalf("default config: %+v", svc.opts.Config)
	}
}

func TestLoadConfigSustained(t *testing.T) {
	read := func(string) ([]byte, error) {
		return []byte("sustained:\n  cpu_pct: 70\n  window: 30m\n  allowlist: [\"Backupd*\", mds_stores]\n"), nil
	}
	c, err := LoadConfig("x", read)
	if err != nil || c.Sustained.CPUPct != 70 || c.Sustained.Window != 30*time.Minute || c.Sustained.MinFraction != 0.9 || len(c.Sustained.Allowlist) != 2 {
		t.Fatalf("sustained config: %+v %v", c.Sustained, err)
	}
	bad := func(string) ([]byte, error) {
		return []byte("sustained:\n  cpu_pct: 0\n  window: 5s\n  min_fraction: 0.2\n"), nil
	}
	c, err = LoadConfig("x", bad)
	if err == nil || c.Sustained.CPUPct != 50 || c.Sustained.Window != 15*time.Minute || c.Sustained.MinFraction != 0.9 {
		t.Fatalf("invalid sustained config not reset: %+v %v", c.Sustained, err)
	}
}
