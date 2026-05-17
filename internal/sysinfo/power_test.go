package sysinfo

import (
	"errors"
	"testing"
	"time"
)

const battOnBattery = `Now drawing from 'Battery Power'
 -InternalBattery-0 (id=4718601)	85%; discharging; 3:42 remaining present: true
`

const battOnAC = `Now drawing from 'AC Power'
 -InternalBattery-0 (id=4718601)	100%; charged; 0:00 remaining present: true
`

const thermNominal = `System thermal state: nominal
CPU_Speed_Limit	= 100
`

const thermSerious = `System thermal state: serious
CPU_Speed_Limit	= 60
`

const thermLogThrottled = `2021-07-07 12:32:50 -0400 CPU Power notify
	CPU_Scheduler_Limit 	= 100
	CPU_Available_CPUs 	= 8
	CPU_Speed_Limit 	= 90
2021-07-07 12:32:51 -0400 CPU Power notify
	CPU_Scheduler_Limit 	= 100
	CPU_Available_CPUs 	= 8
	CPU_Speed_Limit 	= 92

2021-07-07 12:32:52 -0400 CPU Power notify
	CPU_Scheduler_Limit 	= 100
	CPU_Available_CPUs 	= 8
	CPU_Speed_Limit 	= 100
`

const thermLogWithGarbage = `2021-07-07 12:32:50 -0400 CPU Power notify
	CPU_Scheduler_Limit 	= 100
	CPU_Available_CPUs 	= 8
	CPU_Speed_Limit 	= 90
2021-07-07 12:32:51 -0400 not a complete record
	CPU_Speed_Limit 	= nope
2021-07-07 12:32:52 -0400 CPU Power notify
	CPU_Available_CPUs 	= 8
	CPU_Speed_Limit 	= 100
`

const assertionsOutput = `Assertion status system-wide:
   BackgroundTask                 0
   PreventUserIdleSleep           1
   PreventUserIdleDisplaySleep    0
   PreventSystemSleep             0

Listed by owning process:
 pid 412(Slack): [0x000165b8000002f1] 00:00:04 PreventUserIdleSleep named: "playing audio"
`

const topEnergyOutput = `PID    POWER  COMMAND
99647  12.5   Slack
412    3.2    com.apple.WebKit
1      0.0    launchd
`

const topEnergyOutputWithSpaces = `PID    POWER  COMMAND
501    4.2    Google Chrome Helper
`

func stubPower(batt, therm, assertions, topEnergy string) CmdRunner {
	return func(name string, args ...string) ([]byte, error) {
		if name == "top" {
			if topEnergy == "" {
				return nil, errors.New("no top")
			}
			return []byte(topEnergy), nil
		}
		if name != "pmset" || len(args) < 2 {
			return nil, errors.New("unexpected")
		}
		switch args[1] {
		case "batt":
			if batt == "" {
				return nil, errors.New("no batt")
			}
			return []byte(batt), nil
		case "therm":
			if therm == "" {
				return nil, errors.New("no therm")
			}
			return []byte(therm), nil
		case "assertions":
			if assertions == "" {
				return nil, errors.New("no assertions")
			}
			return []byte(assertions), nil
		}
		return nil, errors.New("unknown pmset arg")
	}
}

func stubPmset(batt, therm, assertions string) CmdRunner {
	return stubPower(batt, therm, assertions, "")
}

type fakePowerSource struct {
	batt       string
	therm      string
	assertions string
	topEnergy  string
}

func (f fakePowerSource) Battery() ([]byte, error) {
	if f.batt == "" {
		return nil, errors.New("no batt")
	}
	return []byte(f.batt), nil
}

func (f fakePowerSource) Thermal() ([]byte, error) {
	if f.therm == "" {
		return nil, errors.New("no therm")
	}
	return []byte(f.therm), nil
}

func (f fakePowerSource) Assertions() ([]byte, error) {
	if f.assertions == "" {
		return nil, errors.New("no assertions")
	}
	return []byte(f.assertions), nil
}

func (f fakePowerSource) EnergyTop() ([]byte, error) {
	if f.topEnergy == "" {
		return nil, errors.New("no top")
	}
	return []byte(f.topEnergy), nil
}

func TestCollectPowerOnBattery(t *testing.T) {
	ps := CollectPower(stubPmset(battOnBattery, thermNominal, ""))
	if !ps.OnBattery {
		t.Error("OnBattery = false, want true")
	}
	if ps.BatteryPct != 85 {
		t.Errorf("BatteryPct = %d, want 85", ps.BatteryPct)
	}
	if ps.ThermalPressure != "nominal" {
		t.Errorf("ThermalPressure = %q, want nominal", ps.ThermalPressure)
	}
}

func TestCollectPowerOnAC(t *testing.T) {
	ps := CollectPower(stubPmset(battOnAC, thermNominal, ""))
	if ps.OnBattery {
		t.Error("OnBattery = true, want false")
	}
	if ps.BatteryPct != 100 {
		t.Errorf("BatteryPct = %d, want 100", ps.BatteryPct)
	}
}

func TestCollectPowerThermal(t *testing.T) {
	ps := CollectPower(stubPmset("", thermSerious, ""))
	if ps.ThermalPressure != "serious" {
		t.Errorf("ThermalPressure = %q, want serious", ps.ThermalPressure)
	}
	if !ps.ThermalThrottled {
		t.Error("ThermalThrottled = false, want true")
	}
	if ps.CPUSpeedLimitPct != 60 {
		t.Errorf("CPUSpeedLimitPct = %d, want 60", ps.CPUSpeedLimitPct)
	}
	if ps.PercentThermalThrottled != 100 {
		t.Errorf("PercentThermalThrottled = %d, want 100", ps.PercentThermalThrottled)
	}
}

func TestCollectPowerAssertions(t *testing.T) {
	ps := CollectPower(stubPmset("", "", assertionsOutput))
	if len(ps.Assertions) != 1 {
		t.Fatalf("Assertions = %d, want 1; %+v", len(ps.Assertions), ps.Assertions)
	}
	a := ps.Assertions[0]
	if a.Type != "PreventUserIdleSleep" {
		t.Errorf("Type = %q, want PreventUserIdleSleep", a.Type)
	}
	if a.PID != 412 {
		t.Errorf("PID = %d, want 412", a.PID)
	}
	if a.Name != "playing audio" {
		t.Errorf("Name = %q, want 'playing audio'", a.Name)
	}
}

func TestCollectPowerAllFail(t *testing.T) {
	stub := func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("command not found")
	}
	ps := CollectPower(stub)
	// Should not panic; returns zero value.
	if ps.OnBattery || ps.BatteryPct != 0 {
		t.Errorf("expected zero value on all-fail: %+v", ps)
	}
}

func TestParseEnergyTop(t *testing.T) {
	users := parseEnergyTop(topEnergyOutput)
	if len(users) != 3 {
		t.Fatalf("len = %d, want 3", len(users))
	}
	if users[0].PID != 99647 {
		t.Errorf("PID = %d, want 99647", users[0].PID)
	}
	if users[0].EnergyImpact != 12.5 {
		t.Errorf("EnergyImpact = %v, want 12.5", users[0].EnergyImpact)
	}
	if users[0].Command != "Slack" {
		t.Errorf("Command = %q, want Slack", users[0].Command)
	}
	if users[2].EnergyImpact != 0.0 {
		t.Errorf("EnergyImpact = %v, want 0.0 for launchd", users[2].EnergyImpact)
	}
}

func TestParseEnergyTopPreservesCommandWithSpaces(t *testing.T) {
	users := parseEnergyTop(topEnergyOutputWithSpaces)
	if len(users) != 1 {
		t.Fatalf("len = %d, want 1", len(users))
	}
	if users[0].Command != "Google Chrome Helper" {
		t.Errorf("Command = %q, want Google Chrome Helper", users[0].Command)
	}
}

func TestParseThermAggregatesSpeedLimits(t *testing.T) {
	var ps PowerState
	parseTherm(thermLogThrottled, &ps)
	if !ps.ThermalThrottled {
		t.Error("ThermalThrottled = false, want true")
	}
	if ps.CPUSpeedLimitPct != 100 {
		t.Errorf("CPUSpeedLimitPct = %d, want latest 100", ps.CPUSpeedLimitPct)
	}
	if ps.LowestCPUSpeedLimitPct != 90 {
		t.Errorf("LowestCPUSpeedLimitPct = %d, want 90", ps.LowestCPUSpeedLimitPct)
	}
	if ps.AverageCPUSpeedLimitPct != 94 {
		t.Errorf("AverageCPUSpeedLimitPct = %d, want 94", ps.AverageCPUSpeedLimitPct)
	}
	if ps.PercentThermalThrottled != 66 {
		t.Errorf("PercentThermalThrottled = %d, want 66", ps.PercentThermalThrottled)
	}
	wantSamples := []int{90, 92, 100}
	if len(ps.CPUSpeedLimitSamples) != len(wantSamples) {
		t.Fatalf("samples = %v, want %v", ps.CPUSpeedLimitSamples, wantSamples)
	}
	for i := range wantSamples {
		if ps.CPUSpeedLimitSamples[i] != wantSamples[i] {
			t.Fatalf("samples = %v, want %v", ps.CPUSpeedLimitSamples, wantSamples)
		}
	}
}

func TestParseThermSkipsInvalidSpeedLimits(t *testing.T) {
	var ps PowerState
	parseTherm(thermLogWithGarbage, &ps)
	if len(ps.CPUSpeedLimitSamples) != 2 {
		t.Fatalf("samples = %v, want two valid samples", ps.CPUSpeedLimitSamples)
	}
	if ps.LowestCPUSpeedLimitPct != 90 {
		t.Errorf("LowestCPUSpeedLimitPct = %d, want 90", ps.LowestCPUSpeedLimitPct)
	}
}

func TestCollectPowerEnergyTopUsers(t *testing.T) {
	ps := CollectPower(stubPower("", "", "", topEnergyOutput))
	if len(ps.EnergyTopUsers) != 3 {
		t.Fatalf("EnergyTopUsers = %d, want 3", len(ps.EnergyTopUsers))
	}
	if ps.EnergyTopUsers[1].PID != 412 {
		t.Errorf("second PID = %d, want 412", ps.EnergyTopUsers[1].PID)
	}
}

// slowPowerSource adds a per-probe sleep so we can observe wall-clock fan-out.
type slowPowerSource struct {
	fakePowerSource
	delay time.Duration
}

func (s slowPowerSource) Battery() ([]byte, error) {
	time.Sleep(s.delay)
	return s.fakePowerSource.Battery()
}

func (s slowPowerSource) Thermal() ([]byte, error) {
	time.Sleep(s.delay)
	return s.fakePowerSource.Thermal()
}

func (s slowPowerSource) Assertions() ([]byte, error) {
	time.Sleep(s.delay)
	return s.fakePowerSource.Assertions()
}

func (s slowPowerSource) EnergyTop() ([]byte, error) {
	time.Sleep(s.delay)
	return s.fakePowerSource.EnergyTop()
}

func newSlowPowerSource(delay time.Duration) slowPowerSource {
	return slowPowerSource{
		delay: delay,
		fakePowerSource: fakePowerSource{
			batt:       battOnBattery,
			therm:      thermNominal,
			assertions: assertionsOutput,
			topEnergy:  topEnergyOutput,
		},
	}
}

func TestCollectPowerRunsProbesConcurrently(t *testing.T) {
	const delay = 150 * time.Millisecond
	start := time.Now()
	ps := PowerCollector{Source: newSlowPowerSource(delay)}.Collect()
	elapsed := time.Since(start)

	// Serial would be 4*delay. Generous slack for slow CI; the cap is well
	// below the serial floor.
	if elapsed >= 3*delay {
		t.Errorf("Collect took %v with 4 probes at %v each; expected concurrent execution", elapsed, delay)
	}
	if !ps.OnBattery {
		t.Error("OnBattery = false, want true")
	}
	if ps.ThermalPressure != "nominal" {
		t.Errorf("ThermalPressure = %q, want nominal", ps.ThermalPressure)
	}
	if len(ps.Assertions) != 1 {
		t.Errorf("Assertions = %d, want 1", len(ps.Assertions))
	}
	if len(ps.EnergyTopUsers) != 3 {
		t.Errorf("EnergyTopUsers = %d, want 3", len(ps.EnergyTopUsers))
	}
}

func TestCollectPowerPartialFailure(t *testing.T) {
	ps := PowerCollector{Source: fakePowerSource{
		batt:       battOnBattery,
		assertions: assertionsOutput,
		topEnergy:  topEnergyOutput,
	}}.Collect()
	if !ps.OnBattery {
		t.Error("OnBattery = false, want true")
	}
	if ps.ThermalPressure != "" {
		t.Errorf("ThermalPressure = %q, want empty after failed thermal probe", ps.ThermalPressure)
	}
	if len(ps.Assertions) != 1 {
		t.Errorf("Assertions = %d, want 1", len(ps.Assertions))
	}
	if len(ps.EnergyTopUsers) != 3 {
		t.Errorf("EnergyTopUsers = %d, want 3", len(ps.EnergyTopUsers))
	}
}

func BenchmarkPowerCollect(b *testing.B) {
	collector := PowerCollector{Source: newSlowPowerSource(100 * time.Millisecond)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collector.Collect()
	}
}

func TestPowerCollectorWithFakeSource(t *testing.T) {
	ps := PowerCollector{Source: fakePowerSource{
		batt:       battOnBattery,
		therm:      thermSerious,
		assertions: assertionsOutput,
		topEnergy:  topEnergyOutputWithSpaces,
	}}.Collect()
	if !ps.OnBattery {
		t.Error("OnBattery = false, want true")
	}
	if ps.ThermalPressure != "serious" {
		t.Errorf("ThermalPressure = %q, want serious", ps.ThermalPressure)
	}
	if len(ps.Assertions) != 1 {
		t.Fatalf("Assertions = %d, want 1", len(ps.Assertions))
	}
	if len(ps.EnergyTopUsers) != 1 || ps.EnergyTopUsers[0].Command != "Google Chrome Helper" {
		t.Fatalf("EnergyTopUsers = %+v, want Google Chrome Helper entry", ps.EnergyTopUsers)
	}
}
