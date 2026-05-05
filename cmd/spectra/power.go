package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func runPower(args []string) int {
	fs := flag.NewFlagSet("spectra power", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	state := sysinfo.CollectPower(sysinfo.DefaultRunner)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}

	printPowerState(state)
	return 0
}

func printPowerState(s sysinfo.PowerState) {
	fmt.Println("=== Power state ===")

	if s.OnBattery {
		fmt.Printf("source:    battery (%d%%)\n", s.BatteryPct)
	} else {
		fmt.Printf("source:    AC power\n")
		if s.BatteryPct > 0 {
			fmt.Printf("battery:   %d%% (charging)\n", s.BatteryPct)
		}
	}

	if s.ThermalPressure != "" {
		fmt.Printf("thermal:   %s\n", s.ThermalPressure)
	}

	if len(s.Assertions) > 0 {
		fmt.Println()
		fmt.Printf("assertions (%d):\n", len(s.Assertions))
		for _, a := range s.Assertions {
			name := ""
			if a.Name != "" {
				name = fmt.Sprintf(" %q", a.Name)
			}
			fmt.Printf("  pid %-7d  %-30s%s\n", a.PID, a.Type, name)
		}
	}

	if len(s.EnergyTopUsers) > 0 {
		fmt.Println()
		fmt.Printf("energy top users:\n")
		fmt.Printf("  %-7s  %-8s  %s\n", "PID", "IMPACT", "COMMAND")
		fmt.Println("  " + strings.Repeat("-", 40))
		for _, u := range s.EnergyTopUsers {
			fmt.Printf("  %-7d  %-8.1f  %s\n", u.PID, u.EnergyImpact, u.Command)
		}
	}
}
