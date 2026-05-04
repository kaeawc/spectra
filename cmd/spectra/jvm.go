package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/jvm"
)

func runJVM(args []string) int {
	// Dispatch subcommands before flag parsing so "--help" on a subcommand works.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "thread-dump":
			return runJVMThreadDump(args[1:])
		case "heap-histogram":
			return runJVMHeapHistogram(args[1:])
		case "heap-dump":
			return runJVMHeapDump(args[1:])
		case "jfr":
			return runJVMJFR(args[1:])
		}
	}

	fs := flag.NewFlagSet("spectra jvm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	var infos []jvm.Info

	if fs.NArg() == 0 {
		infos = jvm.CollectAll(ctx, jvm.CollectOptions{})
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, "no running JVMs found (is jps in your PATH?)")
			return 0
		}
	} else {
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

func runJVMThreadDump(args []string) int {
	fs := flag.NewFlagSet("spectra jvm thread-dump", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm thread-dump <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	data, err := jvm.ThreadDump(pid, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "thread-dump failed for PID %d: %v\n", pid, err)
		return 1
	}

	key := cache.Key([]byte(fmt.Sprintf("threads:%d:%d", pid, time.Now().UnixNano())))
	if cacheStores != nil && cacheStores.Threads != nil {
		if putErr := cacheStores.Threads.Put(key, data); putErr == nil {
			fmt.Fprintf(os.Stderr, "cached as threads/%x\n", key[:4])
		}
	}

	os.Stdout.Write(data)
	return 0
}

func runJVMHeapHistogram(args []string) int {
	fs := flag.NewFlagSet("spectra jvm heap-histogram", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm heap-histogram <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	data, err := jvm.HeapHistogram(pid, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "heap-histogram failed for PID %d: %v\n", pid, err)
		return 1
	}

	os.Stdout.Write(data)
	return 0
}

func runJVMHeapDump(args []string) int {
	fs := flag.NewFlagSet("spectra jvm heap-dump", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "Output .hprof path (default: ~/.spectra/<pid>-<ts>.hprof)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm heap-dump [--out <path>] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	destPath := *out
	if destPath == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			fmt.Fprintln(os.Stderr, herr)
			return 1
		}
		ts := time.Now().UTC().Format("20060102T150405Z")
		destPath = filepath.Join(home, ".spectra", fmt.Sprintf("%d-%s.hprof", pid, ts))
		if mkErr := os.MkdirAll(filepath.Dir(destPath), 0o700); mkErr != nil {
			fmt.Fprintln(os.Stderr, mkErr)
			return 1
		}
	}

	fmt.Fprintf(os.Stderr, "writing heap dump to %s ...\n", destPath)
	if err := jvm.HeapDump(pid, destPath, nil); err != nil {
		fmt.Fprintf(os.Stderr, "heap-dump failed for PID %d: %v\n", pid, err)
		return 1
	}
	fmt.Println(destPath)
	return 0
}

func runJVMJFR(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm jfr <start|dump|stop> <pid> [--out <path>] [--name <name>]")
		return 2
	}
	sub := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("spectra jvm jfr "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outPath := fs.String("out", "", "Output .jfr path (for dump/stop)")
	name := fs.String("name", "spectra", "Recording name")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: spectra jvm jfr %s [flags] <pid>\n", sub)
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	switch sub {
	case "start":
		if err := jvm.JFRStart(pid, *name, nil); err != nil {
			fmt.Fprintf(os.Stderr, "JFR.start failed for PID %d: %v\n", pid, err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "JFR recording %q started on PID %d\n", *name, pid)

	case "dump":
		dest := *outPath
		if dest == "" {
			home, herr := os.UserHomeDir()
			if herr != nil {
				fmt.Fprintln(os.Stderr, herr)
				return 1
			}
			ts := time.Now().UTC().Format("20060102T150405Z")
			dest = filepath.Join(home, ".spectra", fmt.Sprintf("%d-%s.jfr", pid, ts))
			if mkErr := os.MkdirAll(filepath.Dir(dest), 0o700); mkErr != nil {
				fmt.Fprintln(os.Stderr, mkErr)
				return 1
			}
		}
		if err := jvm.JFRDump(pid, *name, dest, nil); err != nil {
			fmt.Fprintf(os.Stderr, "JFR.dump failed for PID %d: %v\n", pid, err)
			return 1
		}
		if cacheStores != nil && cacheStores.JFR != nil {
			if data, rerr := os.ReadFile(dest); rerr == nil {
				key := cache.Key([]byte(fmt.Sprintf("jfr:%d:%d", pid, time.Now().UnixNano())))
				_ = cacheStores.JFR.Put(key, data)
			}
		}
		fmt.Println(dest)

	case "stop":
		dest := *outPath
		if err := jvm.JFRStop(pid, *name, dest, nil); err != nil {
			fmt.Fprintf(os.Stderr, "JFR.stop failed for PID %d: %v\n", pid, err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "JFR recording %q stopped on PID %d\n", *name, pid)
		if dest != "" {
			fmt.Println(dest)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown jfr subcommand %q; use start, dump, or stop\n", sub)
		return 2
	}
	return 0
}

func printJVMList(infos []jvm.Info) {
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
