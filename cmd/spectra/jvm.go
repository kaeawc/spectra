package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/jvm"
)

func runJVM(args []string) int {
	fs := flag.NewFlagSet("spectra jvm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	var infos []jvm.Info

	if fs.NArg() == 0 {
		// List all running JVMs.
		infos = jvm.CollectAll(ctx, jvm.CollectOptions{})
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, "no running JVMs found (is jps in your PATH?)")
			return 0
		}
	} else {
		// Inspect a specific PID.
		pidStr := fs.Arg(0)
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid PID %q\n", pidStr)
			return 2
		}
		info := jvm.InspectPID(ctx, pid, jvm.CollectOptions{})
		if info == nil {
			fmt.Fprintf(os.Stderr, "could not inspect PID %d (process not found or jcmd unavailable)\n", pid)
			return 1
		}
		infos = []jvm.Info{*info}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(infos)
		return 0
	}

	if fs.NArg() == 0 {
		printJVMList(infos)
	} else {
		printJVMDetail(infos[0])
	}
	return 0
}

func printJVMList(infos []jvm.Info) {
	// Sort by PID for stable output.
	sort.Slice(infos, func(i, j int) bool { return infos[i].PID < infos[j].PID })

	fmt.Printf("%-7s  %-12s  %-8s  %s\n", "PID", "VERSION", "THREADS", "MAIN CLASS")
	fmt.Println(strings.Repeat("-", 70))
	for _, info := range infos {
		ver := info.JDKVersion
		if ver == "" {
			ver = "?"
		}
		threads := "-"
		if info.ThreadCount > 0 {
			threads = strconv.Itoa(info.ThreadCount)
		}
		mc := info.MainClass
		if mc == "" {
			mc = "(unknown)"
		}
		fmt.Printf("%-7d  %-12s  %-8s  %s\n", info.PID, truncate(ver, 12), threads, mc)
	}
}

func printJVMDetail(info jvm.Info) {
	fmt.Printf("PID           %d\n", info.PID)
	fmt.Printf("Main class    %s\n", strOrDash(info.MainClass))
	fmt.Printf("JDK vendor    %s\n", strOrDash(info.JDKVendor))
	fmt.Printf("JDK version   %s\n", strOrDash(info.JDKVersion))
	fmt.Printf("Java home     %s\n", strOrDash(info.JavaHome))
	fmt.Printf("VM args       %s\n", strOrDash(info.VMArgs))
	fmt.Printf("VM flags      %s\n", strOrDash(info.VMFlags))
	fmt.Printf("Threads       %s\n", intOrDash(info.ThreadCount))

	if len(info.SysProps) > 0 {
		fmt.Println("\nSystem properties:")
		keys := make([]string, 0, len(info.SysProps))
		for k := range info.SysProps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-30s  %s\n", k, info.SysProps[k])
		}
	}
}

func strOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func intOrDash(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}
