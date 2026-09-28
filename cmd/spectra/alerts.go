package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/watchclient"
)

const daemonNotRunning = "spectra daemon is not running (start it with `spectra daemon start` or install it with `spectra daemon install`)"
const daemonAccessDisabled = "daemon access is disabled (--no-daemon or SPECTRA_NO_DAEMON); `spectra alerts` requires the daemon"

type alertClient interface {
	CheckVersion(context.Context, string) error
	Current(context.Context) (hostwatch.Sample, []hostwatch.Alert, error)
	Alerts(context.Context, string, int) ([]hostwatch.Alert, error)
	Ack(context.Context, string) (hostwatch.Alert, error)
	Subscribe(context.Context, bool, func(hostwatch.Event), func(hostwatch.Sample)) error
	Close() error
}

type alertDeps struct {
	connect  func(context.Context) (alertClient, bool)
	disabled func() bool
	context  func() (context.Context, context.CancelFunc)
	now      func() time.Time
}

func defaultAlertDeps() alertDeps {
	return alertDeps{
		connect: func(ctx context.Context) (alertClient, bool) {
			paths, err := daemon.DefaultPaths(os.Getenv, os.UserHomeDir, runtime.GOOS)
			if err != nil {
				return nil, false
			}
			return watchclient.Connect(ctx, os.Getenv, paths)
		},
		disabled: func() bool { return watchclient.Disabled(os.Getenv) },
		context: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt)
		},
		now: time.Now,
	}
}

func runAlerts(args []string) int {
	return runAlertsWithIO(args, os.Stdout, os.Stderr, defaultAlertDeps())
}

type alertOptions struct {
	mode, state, id           string
	limit                     int
	asJSON, samples, noDaemon bool
}

func parseAlertOptions(args []string, stderr io.Writer) (alertOptions, bool) {
	opt := alertOptions{mode: "list"}
	if len(args) > 0 && (args[0] == "ack" || args[0] == "watch" || args[0] == "health") {
		opt.mode, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("alerts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opt.state, "state", "firing", "firing, resolved, or all")
	fs.IntVar(&opt.limit, "limit", 100, "maximum alerts")
	fs.BoolVar(&opt.asJSON, "json", false, "output JSON")
	fs.BoolVar(&opt.samples, "samples", false, "include host samples when watching")
	fs.BoolVar(&opt.noDaemon, "no-daemon", false, "disable daemon access")
	// Allow flags after the ack id as well as before it.
	if opt.mode == "ack" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		opt.id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return opt, false
	}
	if opt.mode == "ack" && opt.id == "" && fs.NArg() == 1 {
		opt.id = fs.Arg(0)
	}
	if !validAlertOptions(opt, fs.Args()) {
		fmt.Fprintln(stderr, "usage: spectra alerts [--state firing|resolved|all] [--limit N] [--json] | ack <id> | watch [--samples] | health")
		return opt, false
	}
	return opt, true
}

func validAlertOptions(opt alertOptions, rest []string) bool {
	if len(rest) > 0 && (opt.mode != "ack" || len(rest) != 1 || opt.id != rest[0]) {
		return false
	}
	if opt.mode == "ack" && opt.id == "" {
		return false
	}
	if opt.limit < 1 || opt.limit > 2000 {
		return false
	}
	return opt.state == "firing" || opt.state == "resolved" || opt.state == "all"
}

func runAlertsWithIO(args []string, out, stderr io.Writer, deps alertDeps) int {
	opt, ok := parseAlertOptions(args, stderr)
	if !ok {
		return 2
	}
	if opt.noDaemon || deps.disabled != nil && deps.disabled() {
		fmt.Fprintln(stderr, daemonAccessDisabled)
		return 1
	}
	ctx, cancel := deps.context()
	defer cancel()
	client, ok := deps.connect(ctx)
	if !ok {
		fmt.Fprintln(stderr, daemonNotRunning)
		return 1
	}
	defer client.Close()
	versionCtx, done := context.WithTimeout(ctx, time.Second)
	if err := client.CheckVersion(versionCtx, version); err != nil {
		fmt.Fprintf(stderr, "warning: %v\n", err)
	}
	done()
	err := runAlertMode(ctx, client, opt, out, deps.now())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func runAlertMode(ctx context.Context, client alertClient, opt alertOptions, out io.Writer, now time.Time) error {
	switch opt.mode {
	case "list":
		alerts, err := client.Alerts(ctx, opt.state, opt.limit)
		if err != nil {
			return err
		}
		return printAlerts(out, alerts, opt.asJSON, now)
	case "ack":
		alert, err := client.Ack(ctx, opt.id)
		if err != nil {
			return err
		}
		return printAlerts(out, []hostwatch.Alert{alert}, opt.asJSON, now)
	case "health":
		sample, alerts, err := client.Current(ctx)
		if err != nil {
			return err
		}
		return printHealth(out, sample, alerts, opt.asJSON)
	case "watch":
		return watchAlerts(ctx, client, opt.samples, opt.asJSON, out)
	}
	return nil
}

func watchAlerts(ctx context.Context, client alertClient, samples, asJSON bool, out io.Writer) error {
	return client.Subscribe(ctx, samples, func(event hostwatch.Event) {
		if asJSON {
			_ = json.NewEncoder(out).Encode(event)
			return
		}
		fmt.Fprintf(out, "%s [%s] %s: %s\n", event.Type, event.Alert.Severity, event.Alert.Title, event.Alert.Detail)
	}, func(sample hostwatch.Sample) {
		if asJSON {
			_ = json.NewEncoder(out).Encode(sample)
			return
		}
		fmt.Fprintf(out, "sample %s load %.2f/%.2f/%.2f memory %s\n", sample.At.Format(time.RFC3339), sample.Load1, sample.Load5, sample.Load15, sample.MemoryPressure)
	})
}

func printAlerts(out io.Writer, alerts []hostwatch.Alert, asJSON bool, now time.Time) error {
	if asJSON {
		return json.NewEncoder(out).Encode(alerts)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tKEY\tTITLE\tDETAIL\tFIRED\tID")
	for _, a := range alerts {
		fired := "unknown"
		if !a.FiredAt.IsZero() {
			fired = now.Sub(a.FiredAt).Round(time.Second).String() + " ago"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.Severity, a.Key, a.Title, a.Detail, fired, a.ID)
	}
	return tw.Flush()
}

func printHealth(out io.Writer, s hostwatch.Sample, alerts []hostwatch.Alert, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(struct {
			Sample       hostwatch.Sample  `json:"sample"`
			ActiveAlerts []hostwatch.Alert `json:"active_alerts"`
		}{s, alerts})
	}
	fmt.Fprintf(out, "Load: %.2f / %.2f / %.2f across %d cores\nMemory: %s, swap %.0f MB, free %.1f%%\nDisk free: %.1f GB\n", s.Load1, s.Load5, s.Load15, s.NCPU, s.MemoryPressure, s.SwapUsedMB, s.MemFreePct, s.DataFreeGB)
	kinds := make([]string, 0, len(s.Kinds))
	for kind := range s.Kinds {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return s.Kinds[kinds[i]].CPUPct > s.Kinds[kinds[j]].CPUPct })
	fmt.Fprintln(out, "Top kinds by CPU:")
	for i, kind := range kinds {
		if i >= 5 {
			break
		}
		fmt.Fprintf(out, "  %s %.1f%%\n", kind, s.Kinds[kind].CPUPct)
	}
	fmt.Fprintln(out, "Top processes:")
	for i, p := range s.TopCPU {
		if i >= 5 {
			break
		}
		fmt.Fprintf(out, "  %s (pid %d) %.1f%% CPU, %.0f MB RSS\n", p.Name, p.PID, p.CPUPct, p.RSSMB)
	}
	fmt.Fprintf(out, "Active alerts: %d\n", len(alerts))
	for _, a := range alerts {
		fmt.Fprintf(out, "  [%s] %s: %s\n", a.Severity, a.Title, a.Detail)
	}
	return nil
}
