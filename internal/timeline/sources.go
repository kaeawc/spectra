package timeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/updates"
)

// Report collects the events in the window ending at now and reaching back
// since. It is what `spectra timeline --json` prints and what the daemon's
// timeline.report returns, so both build it here.
func Report(ctx context.Context, since time.Duration, now time.Time, sources ...Source) (Timeline, []error) {
	return Collect(ctx, now.Add(-since), sources...)
}

// HostSources returns the production sources for kind. Process starts work
// everywhere; the unified log and install history are macOS-only, so
// elsewhere they are left out with a note rather than failing on every run.
// procOpts configures the process collector (a ctx-honoring Runner makes it
// killable).
func HostSources(kind hostos.Kind, procOpts process.CollectOptions) ([]Source, string) {
	sources := []Source{ProcessStartSource{Procs: func(ctx context.Context) []process.Info {
		return process.CollectAll(ctx, procOpts)
	}}}
	if hostos.Resolve(kind) != hostos.Darwin {
		return sources, fmt.Sprintf("unified-log errors and install history are macOS-only; showing process starts only (%s)", hostos.Resolve(kind))
	}
	return append(sources,
		LogSource{Run: logquery.Run},
		InstallSource{Fetch: defaultInstallFetch},
	), ""
}

// ProcessStartSource reports processes that started inside the window.
type ProcessStartSource struct {
	Procs func(ctx context.Context) []process.Info
}

func (ProcessStartSource) Name() string { return "process" }

func (s ProcessStartSource) Events(ctx context.Context, since time.Time) ([]Event, error) {
	var out []Event
	for _, p := range s.Procs(ctx) {
		if p.StartTime.IsZero() || p.StartTime.Before(since) {
			continue
		}
		name := p.Command
		if name == "" {
			name = p.BSDName
		}
		out = append(out, Event{
			Time:     p.StartTime,
			Source:   "process",
			Severity: SeverityInfo,
			Summary:  fmt.Sprintf("%s [%d] started", name, p.PID),
		})
	}
	return out, nil
}

// LogSource reports unified-log errors and faults (macOS).
type LogSource struct {
	Run func(ctx context.Context, q logquery.Query) (logquery.Result, error)
}

func (LogSource) Name() string { return "log" }

func (s LogSource) Events(ctx context.Context, since time.Time) ([]Event, error) {
	res, err := s.Run(ctx, logquery.Query{
		Start:           since,
		MinLevel:        "error",
		AllowLongWindow: true,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(res.Entries))
	for _, e := range res.Entries {
		msg := e.EventMessage
		if e.Process != "" {
			msg = e.Process + ": " + msg
		}
		out = append(out, Event{
			Time:     e.Timestamp,
			Source:   "log",
			Severity: logSeverity(e),
			Summary:  msg,
		})
	}
	return out, nil
}

func logSeverity(e logquery.LogEntry) Severity {
	kind := strings.ToLower(e.MessageType)
	if kind == "" {
		kind = strings.ToLower(e.LogType)
	}
	// Exact match: "default" contains "fault" as a substring, so Contains is wrong.
	if kind == "fault" || kind == "error" {
		return SeverityError
	}
	return SeverityWarn
}

// InstallSource reports software installs (macOS install history).
type InstallSource struct {
	Fetch func(ctx context.Context) ([]updates.InstallEntry, error)
}

func (InstallSource) Name() string { return "update" }

func (s InstallSource) Events(ctx context.Context, since time.Time) ([]Event, error) {
	entries, err := s.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range entries {
		if e.InstallDate.IsZero() || e.InstallDate.Before(since) {
			continue
		}
		summary := "installed " + e.Name
		if e.Version != "" {
			summary += " " + e.Version
		}
		out = append(out, Event{
			Time:     e.InstallDate,
			Source:   "update",
			Severity: SeverityInfo,
			Summary:  summary,
		})
	}
	return out, nil
}

func defaultInstallFetch(ctx context.Context) ([]updates.InstallEntry, error) {
	history, err := updates.Collect(ctx)
	if err != nil {
		return nil, err
	}
	return history.Entries, nil
}
