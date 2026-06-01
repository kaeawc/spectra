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
	includeSnapshots := fs.Bool("snapshots", false, "Include APFS snapshots")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	state := storagestate.Collect(storagestate.CollectOptions{IncludeSnapshots: *includeSnapshots})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}

	printStorageState(state, *includeSnapshots)
	return 0
}

func printStorageState(s storagestate.State, includeSnapshots bool) {
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
			if includeSnapshots {
				printVolumeSnapshots(v)
			}
		}
	}

	if len(s.Mounts) > 0 {
		printMounts(s.Mounts)
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

func printMounts(mounts []storagestate.Mount) {
	fmt.Printf("\nmounts (%d):\n", len(mounts))
	fmt.Printf("  %-28s  %-6s  %-9s  %10s  %6s  %s\n", "MOUNT", "FS", "ROLE", "USED", "PCT", "FLAGS")
	fmt.Println("  " + strings.Repeat("-", 82))
	for _, m := range mounts {
		fmt.Printf("  %-28s  %-6s  %-9s  %10s  %5.0f%%  %s\n",
			truncate(m.MountPoint, 28),
			m.FSType,
			m.APFSRole,
			humanSizeUint(m.Capacity.Used),
			m.Capacity.UsedPercent,
			strings.Join(m.Flags, ","),
		)
	}
}

func printVolumeSnapshots(v storagestate.Volume) {
	if len(v.Snapshots) == 0 {
		if strings.EqualFold(v.FSType, "apfs") {
			fmt.Println("    snapshots: none")
		}
		return
	}
	fmt.Printf("    snapshots (%d):\n", len(v.Snapshots))
	for _, snap := range v.Snapshots {
		created := ""
		if !snap.CreatedAt.IsZero() {
			created = " " + snap.CreatedAt.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("      %-12s  %s%s\n", snap.Kind.String(), snap.Name, created)
	}
}

func humanSizeUint(n uint64) string {
	const k = 1024
	switch {
	case n >= k*k*k:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(k*k))
	case n >= k:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(k))
	}
	return fmt.Sprintf("%d B", n)
}
