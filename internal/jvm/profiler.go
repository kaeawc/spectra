package jvm

import "fmt"

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
	run := opts.CmdRunner
	if run == nil {
		run = DefaultRunner
	}
	asprof := opts.AsprofPath
	if asprof == "" {
		asprof = "asprof"
	}
	event := opts.Event
	if event == "" {
		event = "cpu"
	}
	duration := opts.DurationSeconds
	if duration == 0 {
		duration = 30
	}
	if duration < 0 {
		return fmt.Errorf("flamegraph duration must be greater than zero")
	}
	_, err := run(asprof, "-d", fmt.Sprint(duration), "-e", event, "-f", opts.OutputPath, fmt.Sprint(pid))
	return err
}
