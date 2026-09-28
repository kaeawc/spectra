package hostwatch

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
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
}

func DefaultConfig() Config {
	var c Config
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
	return c, nil
}
