package whatswrong

import (
	"context"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/xpcattr"
)

const (
	// xpcBusyCPUPct is the CPU level at which a service is sampled for XPC
	// client attribution.
	xpcBusyCPUPct = 80
	// xpcSampleDuration is the sample length; xpcTimeout bounds the whole
	// attribution so whatswrong is never delayed beyond it.
	xpcSampleDuration = time.Second
	xpcTimeout        = 4 * time.Second
)

// XPCAttributeFunc captures an attribution for a busy pid.
type XPCAttributeFunc func(ctx context.Context, pid int, procs []process.Info) (xpcattr.Attribution, error)

func DefaultXPCAttribute(ctx context.Context, pid int, procs []process.Info) (xpcattr.Attribution, error) {
	return xpcattr.Capture(ctx, xpcattr.Options{
		PID:      pid,
		Duration: xpcSampleDuration,
		Procs:    func(context.Context) []process.Info { return procs },
	})
}

// appendXPCCauses adds a cause naming the client driving the busiest process
// when that process looks like an XPC service and has identifiable clients.
// It samples only on macOS, only for a service already over xpcBusyCPUPct,
// and never longer than xpcTimeout; any failure leaves the causes unchanged.
// A nil attribute disables it.
func appendXPCCauses(ctx context.Context, log logger.Logger, causes []Cause, procs []process.Info, attribute XPCAttributeFunc) []Cause {
	if attribute == nil || hostos.Current() != hostos.Darwin {
		return causes
	}
	top, ok := topByCPU(procs)
	if !ok || top.CPUPct < xpcBusyCPUPct || !xpcattr.LooksLikeService(top) {
		return causes
	}
	ctx, cancel := context.WithTimeout(ctx, xpcTimeout)
	defer cancel()
	a, err := attribute(ctx, top.PID, procs)
	if err != nil {
		log.Debug("whatswrong xpc attribution unavailable", "error", err)
		return causes
	}
	if a.Name == "" {
		a.Name = top.Command
	}
	summary := a.Summary()
	if summary == "" {
		return causes
	}
	return append(causes, Cause{Severity: "medium", Title: "A busy service is being driven by another process", Detail: summary})
}
