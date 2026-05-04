package sysinfo

import (
	"errors"
	"testing"
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

const assertionsOutput = `Assertion status system-wide:
   BackgroundTask                 0
   PreventUserIdleSleep           1
   PreventUserIdleDisplaySleep    0
   PreventSystemSleep             0

Listed by owning process:
 pid 412(Slack): [0x000165b8000002f1] 00:00:04 PreventUserIdleSleep named: "playing audio"
`

func stubPmset(batt, therm, assertions string) CmdRunner {
	return func(name string, args ...string) ([]byte, error) {
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
