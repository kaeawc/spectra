package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/toolchain"
)

func runToolchain(args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "brew":
			return runToolchainBrew(args[1:])
		case "jdk", "jdks":
			return runToolchainJDKs(args[1:])
		}
	}

	fs := flag.NewFlagSet("spectra toolchain", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	tc := toolchain.Collect(context.Background(), toolchain.CollectOptions{})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(tc)
		return 0
	}

	printToolchains(tc)
	return 0
}

func runToolchainBrew(args []string) int {
	fs := flag.NewFlagSet("spectra toolchain brew", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	tc := toolchain.Collect(context.Background(), toolchain.CollectOptions{})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(tc.Brew)
		return 0
	}

	b := tc.Brew
	fmt.Printf("brew formulae: %d\n", len(b.Formulae))
	fmt.Printf("brew casks:    %d\n", len(b.Casks))
	fmt.Printf("brew taps:     %d\n", len(b.Taps))

	if len(b.Formulae) > 0 {
		fmt.Println()
		fmt.Printf("%-30s  %s\n", "FORMULA", "VERSION")
		fmt.Println(strings.Repeat("-", 50))
		for _, f := range b.Formulae {
			flags := ""
			if f.Pinned {
				flags += " [pinned]"
			}
			if f.Deprecated {
				flags += " [deprecated]"
			}
			fmt.Printf("%-30s  %s%s\n", f.Name, f.Version, flags)
		}
	}
	if len(b.Casks) > 0 {
		fmt.Println()
		fmt.Printf("%-30s  %s\n", "CASK", "VERSION")
		fmt.Println(strings.Repeat("-", 50))
		for _, c := range b.Casks {
			fmt.Printf("%-30s  %s\n", c.Name, c.Version)
		}
	}
	return 0
}

func runToolchainJDKs(args []string) int {
	fs := flag.NewFlagSet("spectra toolchain jdks", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	tc := toolchain.Collect(context.Background(), toolchain.CollectOptions{})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(tc.JDKs)
		return 0
	}

	if len(tc.JDKs) == 0 {
		fmt.Fprintln(os.Stderr, "no JDKs found")
		return 0
	}
	fmt.Printf("%-8s  %-12s  %-10s  %-8s  %s\n", "MAJOR", "VERSION", "VENDOR", "SOURCE", "PATH")
	fmt.Println(strings.Repeat("-", 80))
	for _, j := range tc.JDKs {
		active := ""
		if j.IsActiveJavaHome {
			active = " *"
		}
		fmt.Printf("%-8d  %-12s  %-10s  %-8s  %s%s\n",
			j.VersionMajor, truncate(j.ReleaseString, 12),
			truncate(j.Vendor, 10), j.Source, j.Path, active)
	}
	return 0
}

func printToolchains(tc toolchain.Toolchains) {
	fmt.Println("=== Toolchain inventory ===")

	if len(tc.JDKs) > 0 {
		fmt.Printf("\nJDKs (%d):\n", len(tc.JDKs))
		for _, j := range tc.JDKs {
			active := ""
			if j.IsActiveJavaHome {
				active = " (JAVA_HOME)"
			}
			fmt.Printf("  JDK %-4d  %-16s  %-8s  %s%s\n",
				j.VersionMajor, truncate(j.ReleaseString, 16), j.Source, j.Path, active)
		}
	}

	printRuntimes("Node", tc.Node)
	printRuntimes("Python", tc.Python)
	printRuntimes("Go", tc.Go)
	printRuntimes("Ruby", tc.Ruby)

	if len(tc.Rust) > 0 {
		fmt.Printf("\nRust (%d toolchains):\n", len(tc.Rust))
		for _, r := range tc.Rust {
			def := ""
			if r.Default {
				def = " (default)"
			}
			fmt.Printf("  %-30s  %s%s\n", r.Toolchain, r.Channel, def)
		}
	}

	if len(tc.BuildTools) > 0 {
		fmt.Printf("\nBuild tools (%d):\n", len(tc.BuildTools))
		for _, bt := range tc.BuildTools {
			fmt.Printf("  %-8s  %-12s  %s\n", bt.Name, bt.Version, bt.Source)
		}
	}

	if len(tc.JVMManagers) > 0 {
		active := ""
		if tc.ActiveJVMManager != "" {
			active = fmt.Sprintf(" (active: %s)", tc.ActiveJVMManager)
		}
		fmt.Printf("\nJVM managers: %s%s\n", strings.Join(tc.JVMManagers, ", "), active)
	}

	b := tc.Brew
	fmt.Printf("\nHomebrew: %d formulae, %d casks, %d taps\n",
		len(b.Formulae), len(b.Casks), len(b.Taps))

	if tc.Env.JavaHome != "" {
		fmt.Printf("\nJAVA_HOME: %s\n", tc.Env.JavaHome)
	}
	if tc.Env.GoRoot != "" {
		fmt.Printf("GOROOT:    %s\n", tc.Env.GoRoot)
	}
	if tc.Env.GoPath != "" {
		fmt.Printf("GOPATH:    %s\n", tc.Env.GoPath)
	}
}

func printRuntimes(name string, runtimes []toolchain.RuntimeInstall) {
	if len(runtimes) == 0 {
		return
	}
	fmt.Printf("\n%s (%d):\n", name, len(runtimes))
	for _, r := range runtimes {
		active := ""
		if r.Active {
			active = " (active)"
		}
		fmt.Printf("  %-12s  %-8s  %s%s\n", r.Version, r.Source, r.Path, active)
	}
}
