package sysinfo

import (
	"regexp"
	"strconv"
	"strings"
)

// PowerState captures battery and thermal facts.
// See docs/design/system-inventory.md#powerstate.
type PowerState struct {
	OnBattery       bool           `json:"on_battery"`
	BatteryPct      int            `json:"battery_pct"`
	ThermalPressure string         `json:"thermal_pressure,omitempty"` // "nominal", "fair", "serious", "critical"
	Assertions      []PowerAssertion `json:"assertions,omitempty"`
	EnergyTopUsers  []EnergyUser   `json:"energy_top_users,omitempty"`
}

// EnergyUser is one entry from `top -l 1 -o power`.
type EnergyUser struct {
	PID           int     `json:"pid"`
	EnergyImpact  float64 `json:"energy_impact"`
	Command       string  `json:"command"`
}

// PowerAssertion is one pmset sleep/display assertion.
type PowerAssertion struct {
	Type    string `json:"type"`
	PID     int    `json:"pid"`
	Name    string `json:"name,omitempty"`
}

// CollectPower gathers PowerState from pmset. Any sub-command failure is
// silently absorbed; partial results are still valid.
func CollectPower(run CmdRunner) PowerState {
	var ps PowerState

	if out, err := run("pmset", "-g", "batt"); err == nil {
		parseBatt(string(out), &ps)
	}
	if out, err := run("pmset", "-g", "therm"); err == nil {
		parsTherm(string(out), &ps)
	}
	if out, err := run("pmset", "-g", "assertions"); err == nil {
		ps.Assertions = parseAssertions(string(out))
	}
	if out, err := run("top", "-l", "1", "-n", "10", "-o", "power", "-stats", "pid,power,command"); err == nil {
		ps.EnergyTopUsers = parseEnergyTop(string(out))
	}

	return ps
}

// parseEnergyTop parses `top -l 1 -n 10 -o power -stats pid,power,command` output.
func parseEnergyTop(out string) []EnergyUser {
	var result []EnergyUser
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] == "PID" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		impact, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		result = append(result, EnergyUser{PID: pid, EnergyImpact: impact, Command: fields[2]})
	}
	return result
}

// parseBatt extracts OnBattery + BatteryPct from `pmset -g batt` output.
//
// Example line: "Now drawing from 'Battery Power'"
// Percentage line: " -InternalBattery-0 (id=...)	85%; discharging; ..."
func parseBatt(out string, ps *PowerState) {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Battery Power") {
			ps.OnBattery = true
		}
		if pct := extractPct(line); pct >= 0 {
			ps.BatteryPct = pct
		}
	}
}

var pctRe = regexp.MustCompile(`(\d+)%`)

func extractPct(line string) int {
	m := pctRe.FindStringSubmatch(line)
	if len(m) < 2 {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// parsTherm extracts thermal pressure from `pmset -g therm`.
//
// Example: "CPU_Speed_Limit	= 100" and "System thermal state: nominal"
func parsTherm(out string, ps *PowerState) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "System thermal state:") {
			ps.ThermalPressure = strings.TrimSpace(strings.TrimPrefix(line, "System thermal state:"))
			return
		}
		// Fallback: some macOS versions print "thermal state: X"
		if strings.HasPrefix(line, "thermal state:") {
			ps.ThermalPressure = strings.TrimSpace(strings.TrimPrefix(line, "thermal state:"))
			return
		}
	}
}

// parseAssertions extracts active assertions from `pmset -g assertions`.
//
// Real output line format (per-process listing):
//   " pid 412(Slack): [0x000165b8000002f1] 00:00:04 PreventUserIdleSleep named: "audio" "
var (
	assertionPIDRe  = regexp.MustCompile(`pid (\d+)\(`)
	assertionTypeRe = regexp.MustCompile(`\]\s+[\d:]+\s+(\w+)`)
	assertionNameRe = regexp.MustCompile(`named:\s+"([^"]*)"`)
)

func parseAssertions(out string) []PowerAssertion {
	var result []PowerAssertion
	for _, line := range strings.Split(out, "\n") {
		pidM := assertionPIDRe.FindStringSubmatch(line)
		if len(pidM) < 2 {
			continue
		}
		typeM := assertionTypeRe.FindStringSubmatch(line)
		if len(typeM) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(pidM[1])
		a := PowerAssertion{Type: typeM[1], PID: pid}
		if nameM := assertionNameRe.FindStringSubmatch(line); len(nameM) >= 2 {
			a.Name = nameM[1]
		}
		result = append(result, a)
	}
	return result
}
