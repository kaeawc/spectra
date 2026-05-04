package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func runSnapshot(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	withNetwork := fs.Bool("network", false, "Extract embedded URL hosts (slower; scans app.asar)")
	skipApps := fs.Bool("no-apps", false, "Skip the apps inventory; capture host info only")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := snapshot.Options{
		SpectraVersion: version,
		DetectOpts:     detect.Options{ScanNetwork: *withNetwork},
	}
	if *skipApps {
		// Sentinel: empty AppPaths AND a flag to skip auto-discovery.
		// Build() interprets nil/empty as "scan /Applications"; we need
		// a one-element no-op. Use a path that won't exist so the
		// detect call drops it.
		opts.AppPaths = []string{"/dev/null/__skip_apps_marker__"}
	}
	snap := snapshot.Build(context.Background(), opts)
	if *skipApps {
		snap.Apps = nil
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snap)
		return 0
	}
	printSnapshot(snap)
	return 0
}

func printSnapshot(s snapshot.Snapshot) {
	fmt.Println("=== Spectra snapshot ===")
	fmt.Printf("id:             %s\n", s.ID)
	fmt.Printf("kind:           %s\n", s.Kind)
	fmt.Printf("taken-at:       %s\n", s.TakenAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Println()
	fmt.Print(s.Host.String())

	if len(s.Apps) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("apps:           %d inspected\n", len(s.Apps))

	// Group by UI verdict for a quick summary line.
	byUI := map[string]int{}
	for _, a := range s.Apps {
		byUI[a.UI]++
	}
	keys := make([]string, 0, len(byUI))
	for k := range byUI {
		keys = append(keys, k)
	}
	sortStrings(keys)
	fmt.Println("by-ui:")
	for _, k := range keys {
		fmt.Printf("  %-26s %d\n", k, byUI[k])
	}
}

// sortStrings is a tiny wrapper to keep the imports tidy in snapshot.go.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
