package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/timemachine"
)

func runTimeMachine(args []string) int {
	fs := flag.NewFlagSet("spectra timemachine", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	state, err := timemachine.Collect(context.Background())
	if err != nil {
		if errors.Is(err, timemachine.ErrNeedsFullDiskAccess) {
			fmt.Fprintln(os.Stderr, timemachine.FullDiskAccessRemediation)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}
	printTimeMachineState(state)
	return 0
}

func printTimeMachineState(s timemachine.TimeMachineState) {
	fmt.Println("=== Time Machine ===")
	status := "idle"
	if s.Status.Running {
		status = fmt.Sprintf("running %.0f%%", s.Status.Percent)
		if s.Status.BackupPhase != "" {
			status += " " + s.Status.BackupPhase
		}
	}
	fmt.Printf("status:          %s\n", status)
	fmt.Printf("auto-backup:     %t\n", s.AutoBackupEnabled)
	fmt.Printf("scheduler:       %s\n", loadedString(s.SchedulerLoaded))

	if len(s.Destinations) == 0 {
		fmt.Println("destinations:    no destinations configured")
	} else {
		fmt.Printf("destinations:    %d configured\n", len(s.Destinations))
		for _, d := range s.Destinations {
			label := firstNonEmpty(d.Name, d.MountPoint, d.URL, d.ID)
			parts := []string{label}
			if d.Kind != "" {
				parts = append(parts, d.Kind)
			}
			if d.MountPoint != "" {
				parts = append(parts, d.MountPoint)
			}
			fmt.Printf("  - %s\n", strings.Join(parts, "  "))
		}
	}

	fmt.Printf("local snapshots: %d\n", len(s.LocalSnapshots))
}

func loadedString(v bool) string {
	if v {
		return "loaded"
	}
	return "not loaded"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "(unnamed)"
}
