package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kaeawc/spectra/internal/xattrinspect"
)

// xattrWalkFileLimit bounds how many files a directory argument expands to.
const xattrWalkFileLimit = 20000

func runXattrInspect(args []string) int {
	return runXattrInspectWithIO(args, os.Stdout, os.Stderr, defaultXattrDeps())
}

func runXattrInspectWithIO(args []string, stdout, stderr io.Writer, deps xattrinspect.Deps) int {
	fs := flag.NewFlagSet("spectra xattr-inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: spectra xattr-inspect [--json] <path>...")
		return 2
	}

	report := xattrinspect.Inspect(fs.Args(), deps)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printXattrReport(stdout, report)
	return 0
}

func printXattrReport(w io.Writer, report xattrinspect.Report) {
	fmt.Fprintf(w, "=== xattr-inspect (%d scanned) ===\n", report.Scanned)
	fmt.Fprintf(w, "quarantined %d | provenance %d | where-froms %d | AppleDouble %d\n",
		report.Quarantined, report.Provenanced, report.WithWhereFroms, report.WithAppleDouble)
	for _, fr := range report.Files {
		if fr.Error != "" {
			fmt.Fprintf(w, "  %s\n    error: %s\n", fr.Path, fr.Error)
			continue
		}
		if len(fr.Attrs) == 0 && !fr.AppleDouble {
			continue // nothing notable; keep the output focused
		}
		fmt.Fprintf(w, "  %s\n", fr.Path)
		for _, a := range xattrinspect.SortedAttrs(fr) {
			fmt.Fprintf(w, "    [%s] %s\n", a.Class, a.Name)
		}
		if fr.Quarantine != nil {
			fmt.Fprintf(w, "    quarantine: agent=%s time=%s flags=%s\n",
				orDashXattr(fr.Quarantine.Agent), orDashXattr(fr.Quarantine.Timestamp), orDashXattr(fr.Quarantine.Flags))
		}
		for _, u := range fr.WhereFroms {
			fmt.Fprintf(w, "    from: %s\n", truncate(u, 96))
		}
		if fr.AppleDouble {
			fmt.Fprintln(w, "    has an AppleDouble ._ sidecar")
		}
	}
}

func orDashXattr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func defaultXattrDeps() xattrinspect.Deps {
	return xattrinspect.Deps{
		Run: func(args ...string) ([]byte, error) {
			return exec.Command("xattr", args...).Output()
		},
		Files: func(root string) ([]string, error) {
			fi, err := os.Stat(root)
			if err != nil {
				return nil, err
			}
			if !fi.IsDir() {
				return []string{root}, nil
			}
			var files []string
			_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || !d.Type().IsRegular() {
					return nil //nolint:nilerr // tolerate unreadable entries; keep scanning
				}
				files = append(files, path)
				if len(files) >= xattrWalkFileLimit {
					return filepath.SkipAll
				}
				return nil
			})
			return files, nil
		},
		Exists: func(path string) bool {
			_, err := os.Lstat(path)
			return err == nil
		},
	}
}
