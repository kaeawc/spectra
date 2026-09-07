package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectSysctlsLinux(t *testing.T) {
	root := t.TempDir()
	// Mirror /proc/sys layout: fs/file-max, kernel/pid_max, vm/swappiness.
	write := func(rel, val string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(val), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("fs/file-max", "9223372036854775807\n")
	write("kernel/pid_max", "4194304\n")
	write("vm/swappiness", "60\n")

	got := collectSysctlsLinux(root)
	if got["fs.file-max"] != "9223372036854775807" {
		t.Errorf("fs.file-max = %q", got["fs.file-max"])
	}
	if got["kernel.pid_max"] != "4194304" {
		t.Errorf("kernel.pid_max = %q", got["kernel.pid_max"])
	}
	if got["vm.swappiness"] != "60" {
		t.Errorf("vm.swappiness = %q", got["vm.swappiness"])
	}
	if _, ok := got["net.core.somaxconn"]; ok {
		t.Error("absent key should be omitted, not empty")
	}
}

func TestCollectPowerLinuxOnBattery(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, fields map[string]string) {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for k, v := range fields {
			if err := os.WriteFile(filepath.Join(d, k), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("AC", map[string]string{"type": "Mains", "online": "0"})
	mk("BAT0", map[string]string{"type": "Battery", "capacity": "72", "status": "Discharging"})

	ps := collectPowerLinux(root)
	if !ps.OnBattery {
		t.Error("OnBattery should be true when discharging and AC offline")
	}
	if ps.BatteryPct != 72 {
		t.Errorf("BatteryPct = %d, want 72", ps.BatteryPct)
	}
}

func TestCollectPowerLinuxOnAC(t *testing.T) {
	root := t.TempDir()
	mkField := func(name, field, val string) {
		d := filepath.Join(root, name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, field), []byte(val+"\n"), 0o644)
	}
	mkField("AC", "type", "Mains")
	mkField("AC", "online", "1")
	mkField("BAT0", "type", "Battery")
	mkField("BAT0", "capacity", "100")
	mkField("BAT0", "status", "Full")

	ps := collectPowerLinux(root)
	if ps.OnBattery {
		t.Error("OnBattery should be false when AC is online")
	}
	if ps.BatteryPct != 100 {
		t.Errorf("BatteryPct = %d, want 100", ps.BatteryPct)
	}
}

func TestCollectPowerLinuxMissingTree(t *testing.T) {
	ps := collectPowerLinux(filepath.Join(t.TempDir(), "nope"))
	if ps.OnBattery || ps.BatteryPct != 0 {
		t.Errorf("missing tree should yield zero PowerState, got %+v", ps)
	}
}
