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
	"time"

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
				fmt.Printf("      [%s] %s\n", m.Language, nativeModuleLabel(m))
				if verbose {
					for _, h := range m.Hints {
						fmt.Printf("           %s\n", h)
					}
					for _, h := range m.RiskHints {
						fmt.Printf("           risk: %s\n", h)
					}
				}
			}
		}
		if verbose && r.Rust != nil {
			printRustInspection(r.Rust)
		}
	}
}

func nativeModuleLabel(m detect.NativeModule) string {
	if m.PackageName == "" {
		return m.Name
	}
	if m.PackageVersion == "" {
		return fmt.Sprintf("%s (%s)", m.Name, m.PackageName)
	}
	return fmt.Sprintf("%s (%s@%s)", m.Name, m.PackageName, m.PackageVersion)
}

func printMeta(r detect.Result) {
	printIdentityMeta(r)
	printSecurityMeta(r)
	printPrivacyMeta(r)
	printSwiftMeta(r)
	printDependencyMeta(r)
	printObjCMeta(r)
	printStructureMeta(r)
	printRuntimeMeta(r)
	printStorageMeta(r)
}

func printRustInspection(r *detect.RustInspection) {
	fmt.Printf("    rust: %s", r.Kind)
	if r.PrimaryBinary != "" {
		fmt.Printf("  binary=%s", r.PrimaryBinary)
	}
	if r.PanicStringHits > 0 {
		fmt.Printf("  panic_strings=%d", r.PanicStringHits)
	}
	fmt.Println()
	if len(r.LinkedFrameworks) > 0 {
		fmt.Printf("    rust frameworks: %s\n", strings.Join(r.LinkedFrameworks, ", "))
	}
	if len(r.NativeModules) > 0 {
		fmt.Printf("    rust modules: %s\n", truncateList(r.NativeModules, 4))
	}
	if len(r.Sidecars) > 0 {
		fmt.Printf("    rust sidecars: %s\n", truncateList(r.Sidecars, 4))
	}
	if len(r.FollowUps) > 0 {
		fmt.Printf("    rust follow-ups: %s\n", truncateList(r.FollowUps, 5))
	}
}

func printIdentityMeta(r detect.Result) {
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
}

func printSecurityMeta(r detect.Result) {
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
}

func printPrivacyMeta(r detect.Result) {
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
}

func printSwiftMeta(r detect.Result) {
	if r.Swift == nil {
		return
	}
	if len(r.Swift.RuntimeLibraries) > 0 {
		fmt.Printf("    swift runtime: %s\n", truncateList(r.Swift.RuntimeLibraries, 5))
	}
	capabilities := []string{}
	if r.Swift.UsesSwiftUI {
		capabilities = append(capabilities, "SwiftUI")
	}
	if r.Swift.UsesAppIntents {
		capabilities = append(capabilities, "AppIntents")
	}
	if r.Swift.UsesScreenCapture {
		capabilities = append(capabilities, "ScreenCaptureKit")
	}
	if len(capabilities) > 0 {
		fmt.Printf("    swift capabilities: %s\n", strings.Join(capabilities, ", "))
	}
	if len(r.Swift.AppGroups) > 0 {
		fmt.Printf("    app groups: %s\n", truncateList(r.Swift.AppGroups, 4))
	}
	if len(r.Swift.AppleFrameworks) > 0 {
		fmt.Printf("    apple frameworks (%d): %s\n", len(r.Swift.AppleFrameworks),
			truncateList(r.Swift.AppleFrameworks, 6))
	}
}

func printDependencyMeta(r detect.Result) {
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
}

func printObjCMeta(r detect.Result) {
	if r.ObjC == nil {
		return
	}
	if r.ObjC.PrincipalClass != "" || r.ObjC.MainNibFile != "" || r.ObjC.MainStoryboardFile != "" {
		parts := []string{}
		if r.ObjC.PrincipalClass != "" {
			parts = append(parts, "principal "+r.ObjC.PrincipalClass)
		}
		if r.ObjC.MainNibFile != "" {
			parts = append(parts, "nib "+r.ObjC.MainNibFile)
		}
		if r.ObjC.MainStoryboardFile != "" {
			parts = append(parts, "storyboard "+r.ObjC.MainStoryboardFile)
		}
		fmt.Printf("    objc: %s\n", strings.Join(parts, ", "))
	}
	if len(r.ObjC.LinkedFrameworks) > 0 {
		fmt.Printf("    objc frameworks (%d): %s\n", len(r.ObjC.LinkedFrameworks),
			truncateList(r.ObjC.LinkedFrameworks, 8))
	}
	if len(r.ObjC.URLSchemes) > 0 {
		fmt.Printf("    url schemes: %s\n", truncateList(r.ObjC.URLSchemes, 6))
	}
	if len(r.ObjC.DocumentTypes) > 0 {
		names := make([]string, 0, len(r.ObjC.DocumentTypes))
		for _, doc := range r.ObjC.DocumentTypes {
			if doc.Name != "" {
				names = append(names, doc.Name)
			}
		}
		if len(names) > 0 {
			fmt.Printf("    document types: %s\n", truncateList(names, 6))
		}
	}
}

func printStructureMeta(r detect.Result) {
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
			labels[i] = loginItemLabel(li)
		}
		fmt.Printf("    login items (%d): %s\n", len(labels), strings.Join(labels, ", "))
	}
}

func loginItemLabel(li detect.LoginItem) string {
	var flags []string
	if li.Daemon {
		flags = append(flags, "daemon")
	} else if li.Scope == "system" {
		flags = append(flags, "system")
	}
	if li.RunAtLoad {
		flags = append(flags, "run-at-load")
	}
	if li.KeepAlive {
		flags = append(flags, "keep-alive")
	}
	if len(flags) == 0 {
		return li.Label
	}
	return li.Label + " (" + strings.Join(flags, ", ") + ")"
}

func printRuntimeMeta(r detect.Result) {
	if len(r.RunningProcesses) > 0 {
		var totalRSS int
		for _, p := range r.RunningProcesses {
			totalRSS += p.RSSKiB
		}
		uptimeStr := ""
		if r.AppStartedAt != nil {
			d := time.Duration(r.AppUptimeSeconds) * time.Second
			uptimeStr = fmt.Sprintf(", uptime %s", formatDuration(d))
		}
		fmt.Printf("    running: %d processes, %s RSS%s\n",
			len(r.RunningProcesses), humanSize(int64(totalRSS)*1024), uptimeStr)
	}
}

func printStorageMeta(r detect.Result) {
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

func formatDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
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
