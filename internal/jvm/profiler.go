package jvm

import (
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/profile"
)

// FlamegraphOptions configures an async-profiler flamegraph capture.
type FlamegraphOptions struct {
	// AsprofPath is the async-profiler CLI path. Empty means "asprof" from PATH.
	AsprofPath string
	// Event is the async-profiler event, for example cpu, wall, alloc, or lock.
	Event string
	// DurationSeconds is the capture length passed to asprof -d.
	DurationSeconds int
	// OutputPath is the flamegraph output path, typically .html or .svg.
	OutputPath string
	// CmdRunner is used for subprocess calls. Nil means DefaultRunner.
	CmdRunner CmdRunner
}

// CaptureFlamegraph runs async-profiler against a target JVM. async-profiler
// loads its own native agent into the JVM, so Spectra treats this as an
// explicit operator action rather than passive inspection.
func CaptureFlamegraph(pid int, opts FlamegraphOptions) error {
	if opts.OutputPath == "" {
		return fmt.Errorf("flamegraph output path is required")
	}
	session, err := profile.NormalizeSession(profile.SessionSpec{
		Target: profile.Target{
			PID:          pid,
			Architecture: profile.ArchitectureJVM,
		},
		Collector: "async-profiler",
		Event:     opts.Event,
		Workflow:  profile.WorkflowSampling,
		Duration:  time.Duration(opts.DurationSeconds) * time.Second,
	})
	if err != nil {
		return err
	}
	run := opts.CmdRunner
	if run == nil {
		run = DefaultRunner
	}
	asprof := opts.AsprofPath
	if asprof == "" {
		asprof = "asprof"
	}
	duration := int(session.Duration / time.Second)
	_, err = run(asprof, "-d", fmt.Sprint(duration), "-e", session.Event, "-f", opts.OutputPath, fmt.Sprint(pid))
	return err
}
