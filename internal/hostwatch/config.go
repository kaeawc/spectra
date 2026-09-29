package hostwatch

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	set  bool
	Load struct {
		WarnMultiple       float64 `yaml:"warn_multiple"`
		CriticalMultiple   float64 `yaml:"critical_multiple"`
		Sustain            int     `yaml:"sustain"`
		TrendMinutes       int     `yaml:"trend_minutes"`
		TrendSlopeMultiple float64 `yaml:"trend_slope_multiple"`
	} `yaml:"load"`
	Memory struct {
		WarnSustain  int     `yaml:"warn_sustain"`
		SwapGrowthMB float64 `yaml:"swap_growth_mb"`
	} `yaml:"memory"`
	Limits struct {
		WarnPct     float64 `yaml:"warn_pct"`
		CriticalPct float64 `yaml:"critical_pct"`
	} `yaml:"limits"`
	Disk struct {
		WarnGB     float64 `yaml:"warn_gb"`
		CriticalGB float64 `yaml:"critical_gb"`
	} `yaml:"disk"`
	Thermal struct {
		Sustain int `yaml:"sustain"`
	} `yaml:"thermal"`
	Kinds struct {
		CPUPct       float64 `yaml:"cpu_pct"`
		CPUSustain   int     `yaml:"cpu_sustain"`
		CountSustain int     `yaml:"count_sustain"`
		Gradle       int     `yaml:"gradle"`
		Simulator    int     `yaml:"simulator"`
		QEMU         int     `yaml:"qemu"`
		Agents       int     `yaml:"agents"`
	} `yaml:"kinds"`
	Spawn struct {
		Interval         time.Duration `yaml:"interval"`
		RateWarn         float64       `yaml:"rate_warn"`
		RateCritical     float64       `yaml:"rate_critical"`
		UIDWarnPct       float64       `yaml:"uid_warn_pct"`
		UIDCriticalPct   float64       `yaml:"uid_critical_pct"`
		TotalWarnPct     float64       `yaml:"total_warn_pct"`
		TotalCriticalPct float64       `yaml:"total_critical_pct"`
	} `yaml:"spawn"`
}

func DefaultConfig() Config {
	c := Config{set: true}
	c.Load.WarnMultiple = 1.5
	c.Load.CriticalMultiple = 3
	c.Load.Sustain = 2
	c.Load.TrendMinutes = 5
	c.Load.TrendSlopeMultiple = 0.5
	c.Memory.WarnSustain = 2
	c.Memory.SwapGrowthMB = 1024
	c.Limits.WarnPct = 70
	c.Limits.CriticalPct = 90
	c.Disk.WarnGB = 20
	c.Disk.CriticalGB = 5
	c.Thermal.Sustain = 2
	c.Kinds.CPUPct = 600
	c.Kinds.CPUSustain = 3
	c.Kinds.CountSustain = 2
	c.Kinds.Gradle = 3
	c.Kinds.Simulator = 3
	c.Kinds.QEMU = 2
	c.Kinds.Agents = 8
	c.Spawn.Interval = 5 * time.Second
	c.Spawn.RateWarn = 40
	c.Spawn.RateCritical = 120
	c.Spawn.UIDWarnPct = 40
	c.Spawn.UIDCriticalPct = 70
	c.Spawn.TotalWarnPct = 50
	c.Spawn.TotalCriticalPct = 80
	return c
}
func LoadConfig(path string, readFile func(string) ([]byte, error)) (Config, error) {
	c := DefaultConfig()
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("watch config: %w", err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err = dec.Decode(&c); err != nil {
		return DefaultConfig(), fmt.Errorf("watch config: %w", err)
	}
	return validateConfig(c)
}

func validateConfig(c Config) (Config, error) {
	d := DefaultConfig()
	var problems []error
	for _, field := range []struct {
		name            string
		value, fallback *float64
	}{
		{"load.warn_multiple", &c.Load.WarnMultiple, &d.Load.WarnMultiple},
		{"load.critical_multiple", &c.Load.CriticalMultiple, &d.Load.CriticalMultiple},
		{"load.trend_slope_multiple", &c.Load.TrendSlopeMultiple, &d.Load.TrendSlopeMultiple},
		{"memory.swap_growth_mb", &c.Memory.SwapGrowthMB, &d.Memory.SwapGrowthMB},
		{"limits.warn_pct", &c.Limits.WarnPct, &d.Limits.WarnPct},
		{"limits.critical_pct", &c.Limits.CriticalPct, &d.Limits.CriticalPct},
		{"disk.warn_gb", &c.Disk.WarnGB, &d.Disk.WarnGB},
		{"disk.critical_gb", &c.Disk.CriticalGB, &d.Disk.CriticalGB},
		{"kinds.cpu_pct", &c.Kinds.CPUPct, &d.Kinds.CPUPct},
		{"spawn.rate_warn", &c.Spawn.RateWarn, &d.Spawn.RateWarn},
		{"spawn.rate_critical", &c.Spawn.RateCritical, &d.Spawn.RateCritical},
		{"spawn.uid_warn_pct", &c.Spawn.UIDWarnPct, &d.Spawn.UIDWarnPct},
		{"spawn.uid_critical_pct", &c.Spawn.UIDCriticalPct, &d.Spawn.UIDCriticalPct},
		{"spawn.total_warn_pct", &c.Spawn.TotalWarnPct, &d.Spawn.TotalWarnPct},
		{"spawn.total_critical_pct", &c.Spawn.TotalCriticalPct, &d.Spawn.TotalCriticalPct},
	} {
		if !(*field.value > 0) {
			problems = append(problems, fmt.Errorf("%s must be positive", field.name))
			*field.value = *field.fallback
		}
	}
	for _, field := range []struct {
		name            string
		value, fallback *int
	}{
		{"load.sustain", &c.Load.Sustain, &d.Load.Sustain},
		{"load.trend_minutes", &c.Load.TrendMinutes, &d.Load.TrendMinutes},
		{"memory.warn_sustain", &c.Memory.WarnSustain, &d.Memory.WarnSustain},
		{"thermal.sustain", &c.Thermal.Sustain, &d.Thermal.Sustain},
		{"kinds.cpu_sustain", &c.Kinds.CPUSustain, &d.Kinds.CPUSustain},
		{"kinds.count_sustain", &c.Kinds.CountSustain, &d.Kinds.CountSustain},
		{"kinds.gradle", &c.Kinds.Gradle, &d.Kinds.Gradle},
		{"kinds.simulator", &c.Kinds.Simulator, &d.Kinds.Simulator},
		{"kinds.qemu", &c.Kinds.QEMU, &d.Kinds.QEMU},
		{"kinds.agents", &c.Kinds.Agents, &d.Kinds.Agents},
	} {
		if *field.value <= 0 {
			problems = append(problems, fmt.Errorf("%s must be positive", field.name))
			*field.value = *field.fallback
		}
	}
	if c.Spawn.Interval < time.Second {
		problems = append(problems, fmt.Errorf("spawn.interval must be at least 1s"))
		c.Spawn.Interval = d.Spawn.Interval
	}
	for _, field := range []struct {
		name           string
		warn, critical *float64
	}{
		{"rate", &c.Spawn.RateWarn, &c.Spawn.RateCritical},
		{"uid_pct", &c.Spawn.UIDWarnPct, &c.Spawn.UIDCriticalPct},
		{"total_pct", &c.Spawn.TotalWarnPct, &c.Spawn.TotalCriticalPct},
	} {
		if *field.critical < *field.warn {
			problems = append(problems, fmt.Errorf("spawn.%s critical threshold must be at least warning", field.name))
			*field.critical = *field.warn
		}
	}
	return c, errors.Join(problems...)
}
