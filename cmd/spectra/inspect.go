package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/kaeawc/spectra/internal/detect"
)

// runInspect is the bundle-inspection subcommand and the default
// dispatch target for flag-only invocations.
func runInspect(args []string) int {
	fs := flag.NewFlagSet("spectra inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human table")
	all := fs.Bool("all", false, "Scan every .app under /Applications (and /Applications/Utilities)")
	verbose := fs.Bool("v", false, "Show detection signals")
	withNetwork := fs.Bool("network", false, "Extract embedded URL hosts (slower; scans app.asar)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	var paths []string
	if *all {
		paths = append(paths, scanAppDir("/Applications")...)
		paths = append(paths, scanAppDir("/Applications/Utilities")...)
	}
	paths = append(paths, fs.Args()...)
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra [inspect] [--json] [--all] [-v] <App.app> [...]")
		return 2
	}
	sort.Strings(paths)

	results := scanInspect(paths, detect.Options{ScanNetwork: *withNetwork})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return 0
	}
	printTable(results, *verbose)
	return 0
}

// scanInspect runs detect.Detect across paths in parallel.
func scanInspect(paths []string, opts detect.Options) []detect.Result {
	type indexed struct {
		i   int
		r   detect.Result
		err error
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int, len(paths))
	out := make(chan indexed, len(paths))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				r, err := detect.DetectWith(paths[i], opts)
				out <- indexed{i: i, r: r, err: err}
			}
		}()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	go func() { wg.Wait(); close(out) }()

	results := make([]detect.Result, len(paths))
	ok := make([]bool, len(paths))
	for v := range out {
		if v.err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", paths[v.i], v.err)
			continue
		}
		results[v.i] = v.r
		ok[v.i] = true
	}
	final := make([]detect.Result, 0, len(paths))
	for i, good := range ok {
		if good {
			final = append(final, results[i])
		}
	}
	return final
}

func scanAppDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".app") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func printTable(rs []detect.Result, verbose bool) {
	const nameW, uiW, rtW, pkgW = 28, 24, 14, 10
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n",
		nameW, "APP", uiW, "UI", rtW, "RUNTIME", pkgW, "PACKAGING", "CONFIDENCE")
	fmt.Println(strings.Repeat("-", nameW+uiW+rtW+pkgW+12+8))
	for _, r := range rs {
		name := strings.TrimSuffix(filepath.Base(r.Path), ".app")
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %s\n",
			nameW, truncate(name, nameW),
			uiW, truncate(r.UI, uiW),
			rtW, truncate(r.Runtime, rtW),
			pkgW, truncate(r.Packaging, pkgW),
			r.Confidence)
		if verbose {
			printMeta(r)
			for _, s := range r.Signals {
				fmt.Printf("    · %s\n", s)
			}
		}
		if len(r.NativeModules) > 0 {
			fmt.Printf("    native modules:\n")
			for _, m := range r.NativeModules {
				fmt.Printf("      [%s] %s\n", m.Language, m.Name)
				if verbose {
					for _, h := range m.Hints {
						fmt.Printf("           %s\n", h)
					}
				}
			}
		}
	}
}

func printMeta(r detect.Result) {
	if r.BundleID != "" {
		ver := r.AppVersion
		if r.BuildNumber != "" && r.BuildNumber != r.AppVersion {
			ver = fmt.Sprintf("%s (build %s)", r.AppVersion, r.BuildNumber)
		}
		fmt.Printf("    id: %s  version: %s\n", r.BundleID, ver)
	}
	if len(r.Architectures) > 0 || r.BundleSizeBytes > 0 {
		fmt.Printf("    arch: %s  size: %s\n",
			strings.Join(r.Architectures, ","), humanSize(r.BundleSizeBytes))
	}
	if r.ElectronVersion != "" {
		fmt.Printf("    electron: %s\n", r.ElectronVersion)
	}
	if r.TeamID != "" || r.MASReceipt {
		extras := ""
		if r.MASReceipt {
			extras = " [Mac App Store]"
		}
		fmt.Printf("    team: %s%s\n", r.TeamID, extras)
	}
	security := []string{}
	if r.HardenedRuntime {
		security = append(security, "hardened-runtime")
	}
	if r.Sandboxed {
		security = append(security, "sandboxed")
	}
	if len(security) > 0 {
		fmt.Printf("    security: %s\n", strings.Join(security, ", "))
	}
	if r.GatekeeperStatus != "" {
		fmt.Printf("    gatekeeper: %s\n", r.GatekeeperStatus)
	}
	if len(r.Entitlements) > 0 {
		fmt.Printf("    entitlements: %s\n", strings.Join(r.Entitlements, ", "))
	}
	if r.SparkleFeedURL != "" {
		fmt.Printf("    sparkle feed: %s\n", r.SparkleFeedURL)
	}
	if len(r.GrantedPermissions) > 0 {
		fmt.Printf("    privacy granted: %s\n", strings.Join(r.GrantedPermissions, ", "))
	}
	if len(r.PrivacyDescriptions) > 0 {
		keys := make([]string, 0, len(r.PrivacyDescriptions))
		for k := range r.PrivacyDescriptions {
			keys = append(keys, strings.TrimPrefix(strings.TrimSuffix(k, "UsageDescription"), "NS"))
		}
		sort.Strings(keys)
		fmt.Printf("    privacy declared: %s\n", strings.Join(keys, ", "))
	}
	if r.Dependencies != nil {
		if len(r.Dependencies.ThirdPartyFrameworks) > 0 {
			fmt.Printf("    frameworks (%d): %s\n", len(r.Dependencies.ThirdPartyFrameworks),
				truncateList(r.Dependencies.ThirdPartyFrameworks, 6))
		}
		if len(r.Dependencies.NPMPackages) > 0 {
			fmt.Printf("    npm pkgs (%d): %s\n", len(r.Dependencies.NPMPackages),
				truncateList(r.Dependencies.NPMPackages, 6))
		}
		if r.Dependencies.JavaJars > 0 {
			fmt.Printf("    java jars: %d\n", r.Dependencies.JavaJars)
		}
	}
	if len(r.NetworkEndpoints) > 0 {
		fmt.Printf("    hosts (%d): %s\n", len(r.NetworkEndpoints),
			truncateList(r.NetworkEndpoints, 8))
	}
	if r.Helpers != nil {
		if len(r.Helpers.HelperApps) > 0 {
			fmt.Printf("    helpers (%d): %s\n", len(r.Helpers.HelperApps), truncateList(r.Helpers.HelperApps, 6))
		}
		if len(r.Helpers.XPCServices) > 0 {
			fmt.Printf("    xpc services (%d): %s\n", len(r.Helpers.XPCServices), truncateList(r.Helpers.XPCServices, 6))
		}
		if len(r.Helpers.Plugins) > 0 {
			fmt.Printf("    plugins (%d): %s\n", len(r.Helpers.Plugins), truncateList(r.Helpers.Plugins, 6))
		}
	}
	if len(r.LoginItems) > 0 {
		labels := make([]string, len(r.LoginItems))
		for i, li := range r.LoginItems {
			suffix := ""
			if li.Daemon {
				suffix = " (daemon)"
			} else if li.Scope == "system" {
				suffix = " (system)"
			}
			labels[i] = li.Label + suffix
		}
		fmt.Printf("    login items (%d): %s\n", len(labels), strings.Join(labels, ", "))
	}
	if len(r.RunningProcesses) > 0 {
		var totalRSS int
		for _, p := range r.RunningProcesses {
			totalRSS += p.RSSKiB
		}
		fmt.Printf("    running: %d processes, %s RSS\n",
			len(r.RunningProcesses), humanSize(int64(totalRSS)*1024))
	}
	if r.Storage != nil {
		parts := []string{}
		add := func(label string, n int64) {
			if n > 0 {
				parts = append(parts, fmt.Sprintf("%s %s", label, humanSize(n)))
			}
		}
		add("appsupport", r.Storage.ApplicationSupport)
		add("caches", r.Storage.Caches)
		add("containers", r.Storage.Containers)
		add("groupcontainers", r.Storage.GroupContainers)
		add("http", r.Storage.HTTPStorages)
		add("webkit", r.Storage.WebKit)
		add("logs", r.Storage.Logs)
		add("prefs", r.Storage.Preferences)
		fmt.Printf("    storage: %s total — %s\n", humanSize(r.Storage.Total), strings.Join(parts, ", "))
	}
}

func truncateList(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:n], ", ") + fmt.Sprintf(", … +%d more", len(items)-n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func humanSize(n int64) string {
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
