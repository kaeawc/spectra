package sysinfo

import (
	"testing"
)

const sonomaTasksPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>elapsed_ns</key>
	<integer>1000000000</integer>
	<key>tasks</key>
	<array>
		<dict>
			<key>pid</key>
			<integer>101</integer>
			<key>name</key>
			<string>WindowServer</string>
			<key>energy_impact</key>
			<real>12.5</real>
			<key>cputime_ns</key>
			<integer>50000000</integer>
			<key>gputime_ns</key>
			<integer>2000000</integer>
			<key>ane_energy</key>
			<integer>0</integer>
			<key>short_timer_wakeups</key>
			<integer>3</integer>
			<key>qos_default_ms_per_s</key>
			<real>500.0</real>
			<key>qos_background_ms_per_s</key>
			<real>20.0</real>
		</dict>
		<dict>
			<key>pid</key>
			<integer>412</integer>
			<key>name</key>
			<string>kernel_task</string>
			<key>energy_impact</key>
			<real>4.2</real>
			<key>cputime_ns</key>
			<integer>10000000</integer>
		</dict>
	</array>
</dict>
</plist>
`

const sequoiaTasksPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>elapsedNs</key>
	<integer>1000000000</integer>
	<key>tasks</key>
	<array>
		<dict>
			<key>pid</key>
			<integer>777</integer>
			<key>name</key>
			<string>com.apple.WebKit</string>
			<key>energyImpact</key>
			<real>2.0</real>
			<key>cputimeNs</key>
			<integer>30000000</integer>
			<key>gputimeNs</key>
			<integer>10000</integer>
			<key>aneEnergy</key>
			<integer>5000</integer>
			<key>shortTimerWakeups</key>
			<integer>11</integer>
			<key>qosDefaultMsPerS</key>
			<real>800.0</real>
			<key>qosBackgroundMsPerS</key>
			<real>0.0</real>
		</dict>
	</array>
</dict>
</plist>
`

func TestParseTasksPlistSonoma(t *testing.T) {
	got, err := ParseTasksPlist([]byte(sonomaTasksPlist))
	if err != nil {
		t.Fatalf("ParseTasksPlist: %v", err)
	}
	if got.ElapsedNs != 1_000_000_000 {
		t.Errorf("ElapsedNs = %d, want 1e9", got.ElapsedNs)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("Tasks = %d, want 2", len(got.Tasks))
	}
	first := got.Tasks[0]
	if first.PID != 101 || first.Command != "WindowServer" {
		t.Errorf("first task = %+v", first)
	}
	if first.EnergyImpact != 12.5 {
		t.Errorf("EnergyImpact = %v, want 12.5", first.EnergyImpact)
	}
	if first.CPUNs != 50_000_000 {
		t.Errorf("CPUNs = %d, want 50000000", first.CPUNs)
	}
	if first.GPUNs != 2_000_000 {
		t.Errorf("GPUNs = %d, want 2000000", first.GPUNs)
	}
	if first.ShortTimerWakeups != 3 {
		t.Errorf("ShortTimerWakeups = %d, want 3", first.ShortTimerWakeups)
	}
	if first.QoSDefaultPct != 50.0 {
		t.Errorf("QoSDefaultPct = %v, want 50.0", first.QoSDefaultPct)
	}
	if first.QoSBackgroundPct != 2.0 {
		t.Errorf("QoSBackgroundPct = %v, want 2.0", first.QoSBackgroundPct)
	}
}

func TestParseTasksPlistSequoiaKeys(t *testing.T) {
	got, err := ParseTasksPlist([]byte(sequoiaTasksPlist))
	if err != nil {
		t.Fatalf("ParseTasksPlist: %v", err)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("Tasks = %d, want 1", len(got.Tasks))
	}
	tk := got.Tasks[0]
	if tk.PID != 777 || tk.Command != "com.apple.WebKit" {
		t.Errorf("task = %+v", tk)
	}
	if tk.EnergyImpact != 2.0 {
		t.Errorf("EnergyImpact = %v, want 2.0", tk.EnergyImpact)
	}
	if tk.CPUNs != 30_000_000 {
		t.Errorf("CPUNs = %d, want 30000000", tk.CPUNs)
	}
	if tk.ANENs != 5000 {
		t.Errorf("ANENs = %d, want 5000", tk.ANENs)
	}
	if tk.ShortTimerWakeups != 11 {
		t.Errorf("ShortTimerWakeups = %d, want 11", tk.ShortTimerWakeups)
	}
	if tk.QoSDefaultPct != 80.0 {
		t.Errorf("QoSDefaultPct = %v, want 80.0", tk.QoSDefaultPct)
	}
}

func TestParseTasksPlistEmpty(t *testing.T) {
	if _, err := ParseTasksPlist([]byte("")); err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseTasksPlistMalformedDictMissingValue(t *testing.T) {
	// A dict with a stray key but no value should not panic; remaining
	// fields parse and zero values are returned for missing ones.
	in := `<plist><dict><key>tasks</key><array><dict><key>pid</key><integer>9</integer></dict></array></dict></plist>`
	got, err := ParseTasksPlist([]byte(in))
	if err != nil {
		t.Fatalf("ParseTasksPlist: %v", err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].PID != 9 {
		t.Fatalf("tasks = %+v", got.Tasks)
	}
	if got.Tasks[0].EnergyImpact != 0 {
		t.Errorf("missing energy_impact should be zero, got %v", got.Tasks[0].EnergyImpact)
	}
}

func TestPowermetricsTasksTopTasks(t *testing.T) {
	p := PowermetricsTasks{Tasks: []TaskPowerSample{
		{PID: 1, EnergyImpact: 1.0},
		{PID: 2, EnergyImpact: 5.0},
		{PID: 3, EnergyImpact: 3.0},
	}}
	top := p.TopTasks(2)
	if len(top) != 2 {
		t.Fatalf("len = %d, want 2", len(top))
	}
	if top[0].PID != 2 || top[1].PID != 3 {
		t.Errorf("top = %+v", top)
	}
	if p.Tasks[0].PID != 1 {
		t.Error("TopTasks must not mutate underlying slice")
	}
}
