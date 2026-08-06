// Package crashready audits whether a machine — and optionally one app — can
// produce a debuggable crash before one happens. It answers the question teams
// usually ask too late: "if this crashes right now, will I get anything I can
// debug?" The evaluation is pure over an injectable Host seam so it is testable
// without touching the real machine.
package crashready

import (
	"fmt"
	"strings"
)

// Status classifies a single readiness check.
type Status string

const (
	StatusOK       Status = "ok"
	StatusWarn     Status = "warn"
	StatusCritical Status = "critical"
)

// Check is one readiness signal with a human explanation and, when it is not
// ok, a concrete fix.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Report is the outcome of a readiness audit.
type Report struct {
	App    string  `json:"app,omitempty"`
	Checks []Check `json:"checks"`
}

// Ready is true when no check is critical.
func (r Report) Ready() bool {
	for _, c := range r.Checks {
		if c.Status == StatusCritical {
			return false
		}
	}
	return true
}

// Counts returns how many checks are warnings and how many are critical.
func (r Report) Counts() (warn, critical int) {
	for _, c := range r.Checks {
		switch c.Status {
		case StatusWarn:
			warn++
		case StatusCritical:
			critical++
		}
	}
	return warn, critical
}

// AppDebug is the subset of an inspected app that crashready reasons about.
// The caller builds it from a detect.Result so this package stays decoupled
// from the detector.
type AppDebug struct {
	Name            string
	HardenedRuntime bool
	GetTaskAllow    bool
}

// Host abstracts the live machine facts crashready inspects.
type Host interface {
	// Sysctl returns the trimmed value of a sysctl key (e.g. "kern.coredump").
	Sysctl(key string) (string, error)
	// CoreRLimit returns the RLIMIT_CORE soft and hard limits.
	CoreRLimit() (soft, hard uint64, err error)
	// CoresDir reports whether /cores exists.
	CoresDir() (exists bool)
	// CrashReporterDialogType returns com.apple.CrashReporter DialogType, or
	// "" when it is unset.
	CrashReporterDialogType() string
}

// rlimInfinity is RLIM_INFINITY as an unsigned value (all bits set).
const rlimInfinity = ^uint64(0)

// Evaluate runs every readiness check against host. app is optional; when
// non-nil its debuggability is checked too.
func Evaluate(host Host, app *AppDebug) Report {
	r := Report{}
	if app != nil {
		r.App = app.Name
	}
	r.Checks = append(r.Checks,
		coredumpCheck(host),
		coreLimitCheck(host),
		coresDirCheck(host),
		dialogTypeCheck(host),
	)
	if app != nil {
		r.Checks = append(r.Checks, appDebugCheck(*app))
	}
	return r
}

func coredumpEnabled(host Host) bool {
	v, err := host.Sysctl("kern.coredump")
	return err == nil && strings.TrimSpace(v) == "1"
}

func coredumpCheck(host Host) Check {
	v, err := host.Sysctl("kern.coredump")
	if err != nil {
		return Check{Name: "kern.coredump", Status: StatusWarn, Detail: "could not read sysctl kern.coredump: " + err.Error()}
	}
	if strings.TrimSpace(v) != "1" {
		return Check{
			Name:   "kern.coredump",
			Status: StatusWarn,
			Detail: "full core dumps are disabled (macOS default) — a crash leaves no core file. Crash reports (.ips) are still written to ~/Library/Logs/DiagnosticReports.",
			Fix:    "Enable cores before you need them: sudo sysctl kern.coredump=1 and raise the size limit with `ulimit -c unlimited`.",
		}
	}
	detail := "full core dumps are enabled"
	if pattern, perr := host.Sysctl("kern.corefile"); perr == nil {
		if p := strings.TrimSpace(pattern); p != "" {
			detail += "; core file pattern " + p
		}
	}
	return Check{Name: "kern.coredump", Status: StatusOK, Detail: detail}
}

func coreLimitCheck(host Host) Check {
	soft, hard, err := host.CoreRLimit()
	if err != nil {
		return Check{Name: "core size limit", Status: StatusWarn, Detail: "could not read RLIMIT_CORE: " + err.Error()}
	}
	if soft == 0 {
		return Check{
			Name:   "core size limit",
			Status: StatusWarn,
			Detail: "RLIMIT_CORE soft limit is 0 — no core is written even when kern.coredump is enabled.",
			Fix:    "Raise it in the launching context: `ulimit -c unlimited`, or set LimitCORE in the launchd job.",
		}
	}
	return Check{Name: "core size limit", Status: StatusOK, Detail: fmt.Sprintf("RLIMIT_CORE soft=%s hard=%s", limitString(soft), limitString(hard))}
}

func coresDirCheck(host Host) Check {
	if host.CoresDir() {
		return Check{Name: "/cores", Status: StatusOK, Detail: "/cores exists — the kernel writes core files here as root"}
	}
	if coredumpEnabled(host) {
		return Check{
			Name:   "/cores",
			Status: StatusWarn,
			Detail: "/cores is absent while kern.coredump is enabled — confirm kern.corefile points at an existing, writable directory.",
			Fix:    "Create it (`sudo mkdir -m 1777 /cores`) or point kern.corefile at a writable path.",
		}
	}
	return Check{Name: "/cores", Status: StatusOK, Detail: "/cores is absent (moot while core dumps are disabled)"}
}

func dialogTypeCheck(host Host) Check {
	dt := host.CrashReporterDialogType()
	detail := "DialogType unset (default)"
	if dt != "" {
		detail = "DialogType=" + dt
	}
	detail += " — crash reports are written to ~/Library/Logs/DiagnosticReports regardless."
	return Check{Name: "CrashReporter", Status: StatusOK, Detail: detail}
}

func appDebugCheck(a AppDebug) Check {
	name := "app: " + a.Name
	switch {
	case a.GetTaskAllow:
		return Check{Name: name, Status: StatusOK, Detail: "carries get-task-allow — a debugger can attach and read its memory, and cores are symbolicable."}
	case a.HardenedRuntime:
		return Check{
			Name:   name,
			Status: StatusWarn,
			Detail: "hardened runtime without get-task-allow — task_for_pid is blocked, so lldb attach fails and cores may be unsymbolicable. Normal for release builds.",
			Fix:    "To debug it, use a build signed with the com.apple.security.get-task-allow entitlement (an Xcode Debug build, or re-sign with it).",
		}
	default:
		return Check{Name: name, Status: StatusOK, Detail: "no hardened runtime — a debugger can generally attach (system apps are still gated by SIP)."}
	}
}

func limitString(v uint64) string {
	if v == rlimInfinity {
		return "unlimited"
	}
	return fmt.Sprintf("%d", v)
}
