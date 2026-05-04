package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/cache"
)

// cacheRegistry is the package-level registry populated at init time.
// Stores register themselves; the CLI queries this registry for stats/clear.
var cacheRegistry = cache.Default

// cacheStores holds the concrete ShardedStore instances once initCacheStores
// has run. Used by subcommands that need direct cache access (e.g. jvm thread-dump).
var cacheStores *cache.Stores

// initCacheStores initialises all ShardedStores into the default registry.
// Called from main before dispatching so every subcommand sees a populated
// registry. Failures are non-fatal: if the cache root can't be created, the
// registry stays empty and cache commands print a useful error.
func initCacheStores() {
	root, err := cache.DefaultRoot()
	if err != nil {
		return
	}
	cacheStores = cache.NewStores(root, cacheRegistry)
}

func runCache(args []string) int {
	subs := []subcommand{
		{"stats", "Show cache statistics", runCacheStats},
		{"clear", "Clear cached data", runCacheClear},
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra cache <stats|clear> [--kind <name>]")
		return 2
	}
	for _, sc := range subs {
		if args[0] == sc.name {
			return sc.run(args[1:])
		}
	}
	fmt.Fprintf(os.Stderr, "unknown cache subcommand %q\n", args[0])
	return 2
}

func runCacheStats(args []string) int {
	fs := flag.NewFlagSet("spectra cache stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	stats, err := cacheRegistry.Stats()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(stats) == 0 {
		fmt.Fprintln(os.Stderr, "no cache kinds registered")
		return 0
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(stats)
		return 0
	}

	fmt.Printf("%-12s  %8s  %12s  %s\n", "KIND", "ENTRIES", "BYTES", "LAST WRITE")
	fmt.Println(strings.Repeat("-", 56))
	for _, s := range stats {
		last := "-"
		if !s.LastWrite.IsZero() {
			last = s.LastWrite.Format("2006-01-02 15:04:05Z")
		}
		fmt.Printf("%-12s  %8d  %12s  %s\n",
			s.Kind, s.Entries, humanSize(s.BytesOnDisk), last)
	}
	return 0
}

func runCacheClear(args []string) int {
	fs := flag.NewFlagSet("spectra cache clear", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kind := fs.String("kind", "", "Clear only this kind (default: all)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := cacheRegistry.Clear(*kind); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *kind == "" {
		fmt.Fprintln(os.Stdout, "cache cleared")
	} else {
		fmt.Fprintf(os.Stdout, "cache kind %q cleared\n", *kind)
	}
	return 0
}
