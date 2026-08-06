package sysload

import "testing"

func fakeRunner(pressure, swap string) Runner {
	return func(name string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "kern.memorystatus_vm_pressure_level" {
			return []byte(pressure), nil
		}
		return []byte(swap), nil
	}
}

func TestCollect(t *testing.T) {
	l := Collect(fakeRunner("4\n", "total = 2048.00M  used = 669.88M  free = 1378.12M  (encrypted)"))
	if l.MemoryPressure != "critical" || l.PressureLevel != 4 {
		t.Errorf("pressure = %q (%d), want critical (4)", l.MemoryPressure, l.PressureLevel)
	}
	if l.SwapUsedMB != 669.88 || l.SwapTotalMB != 2048.00 {
		t.Errorf("swap used/total = %v/%v", l.SwapUsedMB, l.SwapTotalMB)
	}
}

func TestPressureLabels(t *testing.T) {
	cases := map[string]string{"1": "normal", "2": "warn", "4": "critical"}
	for in, want := range cases {
		l := Collect(fakeRunner(in, ""))
		if l.MemoryPressure != want {
			t.Errorf("level %s -> %q, want %q", in, l.MemoryPressure, want)
		}
	}
}

func TestSwapGigabytes(t *testing.T) {
	l := Collect(fakeRunner("1", "total = 4.00G  used = 2.50G  free = 1.50G"))
	if l.SwapUsedMB != 2560 { // 2.5 * 1024
		t.Errorf("swap used = %v MB, want 2560", l.SwapUsedMB)
	}
}

func TestMalformedGraceful(t *testing.T) {
	l := Collect(fakeRunner("not-a-number", "garbage"))
	if l.MemoryPressure != "unknown" {
		t.Errorf("malformed pressure -> %q, want unknown", l.MemoryPressure)
	}
	if l.SwapUsedMB != 0 {
		t.Errorf("malformed swap -> %v, want 0", l.SwapUsedMB)
	}
}
