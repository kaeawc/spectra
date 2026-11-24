package services

import (
	"context"
	"strings"
)

// Feature and OffDarwinHint describe launchd services off macOS.
const (
	Feature       = "launchd services"
	OffDarwinHint = "on Linux, use systemctl list-units"
)

// Filter narrows a job list the way `spectra services` flags do.
type Filter struct {
	// Label keeps jobs whose label contains it, case-insensitively.
	Label string
	// Running keeps jobs with a PID; OnDemand keeps on-demand jobs.
	Running, OnDemand bool
}

// ValidDomain reports whether domain is one List understands by name.
func ValidDomain(domain string) bool {
	switch domain {
	case "", "system", "user", "all":
		return true
	}
	return false
}

// Report lists the jobs and applies f. It is what `spectra services --json`
// prints and what the daemon's services.list returns, so both build it here.
func Report(ctx context.Context, opts Options, f Filter) (LaunchInventory, error) {
	inv, err := List(ctx, opts)
	if err != nil {
		return inv, err
	}
	inv.Jobs = f.Apply(inv.Jobs)
	return inv, nil
}

// Apply returns the jobs f keeps.
func (f Filter) Apply(jobs []LaunchJob) []LaunchJob {
	if f.Label == "" && !f.Running && !f.OnDemand {
		return jobs
	}
	label := strings.ToLower(f.Label)
	out := make([]LaunchJob, 0, len(jobs))
	for _, job := range jobs {
		if label != "" && !strings.Contains(strings.ToLower(job.Label), label) {
			continue
		}
		if f.Running && job.PID == 0 {
			continue
		}
		if f.OnDemand && !job.OnDemand {
			continue
		}
		out = append(out, job)
	}
	return out
}
