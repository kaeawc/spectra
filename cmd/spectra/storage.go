package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/cachetriage"
	"github.com/kaeawc/spectra/internal/dbcheck"
	"github.com/kaeawc/spectra/internal/storagestate"
)

func runStorage(args []string) int {
	if len(args) > 0 && args[0] == "db-check" {
		return runStorageDBCheck(args[1:], os.Stdout, os.Stderr)
	}
	if len(args) > 0 && args[0] == "cache-triage" {
		return runStorageCacheTriage(args[1:], os.Stdout, os.Stderr)
	}
	fs := flag.NewFlagSet("spectra storage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	includeSnapshots := fs.Bool("snapshots", false, "Include APFS snapshots")
	includeSpotlight := fs.Bool("spotlight", false, "Include Spotlight indexing status")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	state := storagestate.Collect(storagestate.CollectOptions{
		IncludeSnapshots: *includeSnapshots,
		IncludeSpotlight: *includeSpotlight,
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}

	printStorageState(state, *includeSnapshots, *includeSpotlight)
	return 0
}

func runStorageDBCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spectra storage db-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: spectra storage db-check [--json] <path>...  (a SQLite file, or a directory to scan)")
		return 2
	}

	paths := resolveDBPaths(fs.Args())
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "no SQLite databases found in the given paths")
		return 1
	}

	report := dbcheck.Check(paths, dbcheck.DefaultDeps())
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printDBCheckReport(stdout, report)
	return 0
}

func runStorageCacheTriage(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spectra storage cache-triage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "usage: spectra storage cache-triage [--json] [<root>]")
		return 2
	}

	root := fs.Arg(0)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "cache-triage: resolve home: %v\n", err)
			return 1
		}
		root = filepath.Join(home, "Library", "Caches")
	}

	report, err := cachetriage.Triage(root, cachetriage.DefaultDeps())
	if err != nil {
		fmt.Fprintf(stderr, "cache-triage: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printCacheTriage(stdout, report)
	return 0
}

func printCacheTriage(w io.Writer, report cachetriage.Report) {
	fmt.Fprintf(w, "=== Cache triage: %s ===\n", report.Root)
	fmt.Fprintf(w, "total %s | reclaimable %s (safe+regenerable) | risky %s held back\n",
		humanSize(report.TotalBytes), humanSize(report.ReclaimableBytes), humanSize(report.RiskyBytes))
	for _, e := range report.Entries {
		fmt.Fprintf(w, "  %-10s %10s  %s\n", e.Class, humanSize(e.SizeBytes), truncate(e.Name, 44))
		if e.Class == cachetriage.ClassRisky && e.Reason != "" {
			fmt.Fprintf(w, "               %s\n", truncate(e.Reason, 64))
		}
	}
}

// resolveDBPaths expands directory arguments into the SQLite files they contain
// and keeps file arguments as-is. A path that cannot be stat'd or a directory
// that cannot be scanned is preserved so db-check reports its error rather than
// silently skipping it or aborting the whole run.
func resolveDBPaths(args []string) []string {
	var paths []string
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil || !fi.IsDir() {
			paths = append(paths, a)
			continue
		}
		found, err := dbcheck.Discover(a, readDBHeader)
		if err != nil {
			paths = append(paths, a)
			continue
		}
		paths = append(paths, found...)
	}
	return paths
}

func readDBHeader(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- inspecting user-supplied database paths
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hdr := make([]byte, 16)
	n, err := io.ReadFull(f, hdr)
	if err != nil && n < 16 {
		return nil, err
	}
	return hdr[:n], nil
}

func printDBCheckReport(stdout io.Writer, report dbcheck.Report) {
	fmt.Fprintf(stdout, "=== SQLite db-check (%d scanned, %d with problems) ===\n", report.Scanned, report.Problems)
	for _, db := range report.Databases {
		if db.Error != "" {
			fmt.Fprintf(stdout, "  %s\n    error: %s\n", db.Path, db.Error)
			continue
		}
		status := "ok"
		if !db.IntegrityOK {
			status = fmt.Sprintf("INTEGRITY (%d)", len(db.IntegrityErrors))
		}
		fmt.Fprintf(stdout, "  %s\n", db.Path)
		fmt.Fprintf(stdout, "    size=%s journal=%s integrity=%s frag=%.1f%% free=%d/%d pages wal=%s\n",
			humanSize(db.SizeBytes), stateOrDash(db.JournalMode), status,
			db.FragmentationPct, db.FreelistCount, db.PageCount, humanSize(db.WALBytes))
		for _, e := range db.IntegrityErrors {
			fmt.Fprintf(stdout, "      - %s\n", truncate(e, 100))
		}
	}
}

func printStorageState(s storagestate.State, includeSnapshots, includeSpotlight bool) {
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

	if includeSpotlight {
		printSpotlight(s.Spotlight)
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

func printSpotlight(volumes []storagestate.SpotlightVolume) {
	fmt.Printf("\nspotlight (%d):\n", len(volumes))
	for _, volume := range volumes {
		progress := ""
		if volume.Progress != nil {
			progress = fmt.Sprintf(" %s %.0f%%", volume.Progress.Phase, volume.Progress.Percent)
		}
		fmt.Printf("  %-28s  %-10s  %s%s\n",
			truncate(volume.MountPoint, 28),
			volume.Status.String(),
			volume.Detail,
			progress,
		)
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
