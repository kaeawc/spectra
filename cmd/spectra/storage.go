package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/storagestate"
)

func runStorage(args []string) int {
	fs := flag.NewFlagSet("spectra storage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	state := storagestate.Collect(storagestate.CollectOptions{})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}

	printStorageState(state)
	return 0
}

func printStorageState(s storagestate.State) {
	fmt.Println("=== Storage state ===")

	if len(s.Volumes) > 0 {
		fmt.Printf("volumes (%d):\n", len(s.Volumes))
		fmt.Printf("  %-28s  %10s  %10s  %10s  %s\n", "MOUNT", "TOTAL", "USED", "AVAIL", "PCT")
		fmt.Println("  " + strings.Repeat("-", 68))
		for _, v := range s.Volumes {
			pct := 0
			if v.TotalBytes > 0 {
				pct = int(float64(v.UsedBytes) / float64(v.TotalBytes) * 100)
			}
			fmt.Printf("  %-28s  %10s  %10s  %10s  %d%%\n",
				truncate(v.MountPoint, 28),
				humanSize(v.TotalBytes),
				humanSize(v.UsedBytes),
				humanSize(v.AvailBytes),
				pct,
			)
		}
	}

	if s.UserLibraryBytes > 0 {
		fmt.Printf("\n~/Library:       %s total\n", humanSize(s.UserLibraryBytes))
		if s.AppCachesBytes > 0 {
			fmt.Printf("  ~/Library/Caches: %s\n", humanSize(s.AppCachesBytes))
		}
	}

	if len(s.LargestApps) > 0 {
		fmt.Printf("\nlargest apps (%d):\n", len(s.LargestApps))
		for _, a := range s.LargestApps {
			fmt.Printf("  %10s  %s\n", humanSize(a.OnDiskBytes), a.Path)
		}
	}
}
