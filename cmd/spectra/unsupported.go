package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/symbolicate"
)

// darwinOnly describes a subcommand with no implementation off macOS.
type darwinOnly struct {
	feature string
	hint    string
}

const linuxStuckProcessHint = "on Linux, `spectra procdiag <pid>` diagnoses a stuck or slow process from /proc"

// darwinOnlyCommands are answered off macOS with one uniform unsupported
// result instead of each command failing its own way (often by trying to
// exec a macOS tool that does not exist).
var darwinOnlyCommands = map[string]darwinOnly{
	"inspect":        {"app-bundle inspection", "app inspection reads macOS .app bundles, Mach-O binaries, code signatures, and the TCC database; it has no equivalent on this OS"},
	"list":           {"app-bundle inspection", "app inspection reads macOS .app bundles, Mach-O binaries, code signatures, and the TCC database; it has no equivalent on this OS"},
	"timemachine":    {"Time Machine", ""},
	"services":       {services.Feature, services.OffDarwinHint},
	"startup":        {"startup items", ""},
	"logs":           {logquery.Feature, logquery.OffDarwinHint},
	"updates":        {"macOS install history", ""},
	"uninstall":      {"app uninstall", ""},
	"schedule":       {"launchd snapshot scheduling", "run `spectra snapshot` from cron or a systemd timer instead"},
	"sample":         {"process sampling (sample(1))", linuxStuckProcessHint},
	"flamegraph":     {"native flamegraphs (sample(1))", linuxStuckProcessHint},
	"hang":           {"main-thread hang classification (sample(1))", linuxStuckProcessHint},
	"spindump":       {"spindump stack reports", linuxStuckProcessHint},
	"vmmap":          {"vmmap memory composition", "on Linux, read /proc/<pid>/smaps_rollup; `spectra procdiag <pid>` covers cgroup memory limits"},
	"vmregions":      {"vmmap region composition", "on Linux, read /proc/<pid>/smaps"},
	"lsmp":           {"Mach port tables", ""},
	"whois":          {"XPC/Mach service attribution", ""},
	"malloc-history": {"malloc stack logging", ""},
	"symbolicate":    {symbolicate.Feature, ""},
}

// gateDarwinOnly answers a macOS-only subcommand off macOS. ok is false
// when the command should run.
func gateDarwinOnly(name string, args []string, stdout, stderr io.Writer) (code int, ok bool) {
	d, found := darwinOnlyCommands[name]
	if !found || hostos.Current() == hostos.Darwin {
		return 0, false
	}
	if name == "inspect" && slices.ContainsFunc(args, isHelpFlag) {
		return 0, false
	}
	return reportUnsupported(name, hostos.Unsupported(d.feature), d.hint, wantsJSON(args), stdout, stderr), true
}

// reportUnsupported prints err as an unsupported-platform result: the
// hostos.UnsupportedReport JSON object on stdout when JSON was asked for,
// else one line on stderr. It exits 1, like any other failed command; the
// JSON's "error": "unsupported_platform" is what tells the cases apart.
func reportUnsupported(cmd string, err error, hint string, asJSON bool, stdout, stderr io.Writer) int {
	r, ok := hostos.ReportCommandUnsupported(cmd, err, hint)
	if !ok {
		fmt.Fprintf(stderr, "spectra %s: %v\n", cmd, err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		_ = enc.Encode(r)
		return 1
	}
	msg := fmt.Sprintf("spectra %s: %s", cmd, r.Message)
	if hint != "" {
		msg += " (" + hint + ")"
	}
	fmt.Fprintln(stderr, msg)
	return 1
}

func wantsJSON(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool {
		switch a {
		case "--json", "-json", "--json=true", "-json=true":
			return true
		}
		return false
	})
}

func isHelpFlag(a string) bool {
	return a == "-h" || a == "--help" || a == "-help"
}
