package uninstall

import (
	"context"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/process"
)

// BuildPlan inspects target (a .app path or a bundle id) and returns
// everything that belongs to it. It reads only; nothing is changed.
func BuildPlan(ctx context.Context, opts Options, target string) (Plan, error) {
	if err := supported(opts); err != nil {
		return Plan{}, err
	}
	o := opts.withDefaults()
	bundle, err := resolveApp(o, target)
	if err != nil {
		return Plan{}, err
	}
	app, err := readIdentity(ctx, o, bundle)
	if err != nil {
		return Plan{}, err
	}
	app.Receipts = receiptIDsFor(ctx, o.Runner, app.Path)
	c := collect(ctx, o, app)
	var warnings []string
	if !c.spotlightOK {
		warnings = append(warnings, spotlightWarning)
	}
	return Plan{
		Schema:        PlanSchema,
		CreatedAt:     o.Now().UTC(),
		Home:          o.Home,
		UID:           o.UID,
		App:           app,
		Items:         c.items,
		Processes:     appProcesses(o.Procs(ctx), app.Path),
		LaunchdLabels: c.labels,
		Limitations:   planLimitations,
		Warnings:      warnings,
	}, nil
}

const spotlightWarning = "Spotlight (mdfind) did not answer, so other copies of the app were looked for only in the app folders; an install elsewhere was not seen. apply --leftovers refuses until Spotlight answers."

// appProcesses returns processes whose executable lives inside bundle.
func appProcesses(procs []process.Info, bundle string) []Process {
	out := []Process{}
	prefix := strings.TrimSuffix(bundle, "/") + "/"
	for _, p := range procs {
		if p.PID <= 1 {
			continue
		}
		if strings.HasPrefix(p.ExecutablePath, prefix) || strings.HasPrefix(p.FullCommandLine, prefix) {
			out = append(out, Process{PID: p.PID, Name: p.Command, StartTime: p.StartTime})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}
