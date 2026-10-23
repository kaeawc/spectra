package main

import (
	"context"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/xpcattr"
)

// xpcBusyCPUPct is the CPU level at which a process is sampled for XPC
// client attribution.
const xpcBusyCPUPct = 80

// xpcAttribute captures an attribution for a busy pid; tests substitute it.
var xpcAttribute = func(ctx context.Context, pid int, procs []process.Info) (xpcattr.Attribution, error) {
	return xpcattr.Capture(ctx, xpcattr.Options{
		PID:      pid,
		Duration: 2 * time.Second,
		Procs:    func(context.Context) []process.Info { return procs },
	})
}

// appendXPCCauses adds a cause naming the client driving the busiest process
// when that process is an XPC service with identifiable clients. It samples
// only on macOS and only for a process already over xpcBusyCPUPct; any
// failure leaves the causes unchanged.
func appendXPCCauses(causes []slowCause, procs []process.Info) []slowCause {
	if hostos.Current() != hostos.Darwin {
		return causes
	}
	top, ok := topByCPU(procs)
	if !ok || top.CPUPct < xpcBusyCPUPct {
		return causes
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := xpcAttribute(ctx, top.PID, procs)
	if err != nil {
		cliLogger.Debug("whatswrong xpc attribution unavailable", "error", err)
		return causes
	}
	if a.Name == "" {
		a.Name = top.Command
	}
	summary := a.Summary()
	if summary == "" {
		return causes
	}
	return append(causes, slowCause{Severity: "medium", Title: "A busy service is being driven by another process", Detail: summary})
}
