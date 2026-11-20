package helper

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/startup"
)

// launchdJob is the part of `launchctl print system/<label>` the helper
// compares with disk.
type launchdJob struct {
	loaded   bool
	path     string
	program  string
	firstArg string
	pid      int
	state    string
}

func (j launchdJob) prog() string {
	if j.program != "" {
		return j.program
	}
	return j.firstArg
}

// parseLaunchctlPrint reads the top-level keys of `launchctl print
// system/<label>`. Keys inside nested blocks (sockets, environment) are
// ignored, so a socket's path cannot pose as the plist path.
func parseLaunchctlPrint(out []byte) launchdJob {
	j := launchdJob{loaded: true}
	depth, inArgs := 0, false
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "}":
			depth--
			inArgs = false
			continue
		case strings.HasSuffix(line, "{"):
			if depth == 1 && strings.TrimSpace(strings.TrimSuffix(line, "{")) == "arguments =" {
				inArgs = true
			}
			depth++
			continue
		}
		if depth == 2 && inArgs && j.firstArg == "" && line != "" {
			j.firstArg = line
			continue
		}
		if depth != 1 {
			continue
		}
		if key, val, ok := strings.Cut(line, " = "); ok {
			j.set(key, val)
		}
	}
	return j
}

func (j *launchdJob) set(key, val string) {
	switch key {
	case "path":
		j.path = val
	case "program":
		j.program = val
	case "pid":
		j.pid, _ = strconv.Atoi(val)
	case "state":
		j.state = val
	}
}

// readJob runs `launchctl print system/<label>`. A job launchd does not know
// is not loaded; any other failure is a fault.
func (s *launchdService) readJob(ctx context.Context, label string) (launchdJob, error) {
	res, err := s.runCmd(ctx, launchctlBin, "print", "system/"+label)
	if err != nil {
		return launchdJob{}, fmt.Errorf("launchctl print system/%s: %w", label, err)
	}
	if res.ExitCode == 0 {
		return parseLaunchctlPrint(res.Stdout), nil
	}
	if res.ExitCode == 113 || strings.Contains(joinOutput(res), "Could not find service") {
		return launchdJob{}, nil
	}
	return launchdJob{}, fmt.Errorf("launchctl print system/%s exited %d: %s", label, res.ExitCode, joinOutput(res))
}

// readState is launchd's full view: loaded state and the disabled override.
func (s *launchdService) readState(ctx context.Context, label string) (LaunchdState, launchdJob, error) {
	job, err := s.readJob(ctx, label)
	if err != nil {
		return LaunchdState{}, job, err
	}
	res, err := s.runCmd(ctx, launchctlBin, "print-disabled", "system")
	if err != nil {
		return LaunchdState{}, job, fmt.Errorf("launchctl print-disabled system: %w", err)
	}
	if res.ExitCode != 0 {
		return LaunchdState{}, job, fmt.Errorf("launchctl print-disabled system exited %d: %s", res.ExitCode, joinOutput(res))
	}
	st := LaunchdState{Disabled: startup.ParsePrintDisabled(res.Stdout)[label]}
	if job.loaded {
		st.Loaded, st.PID, st.Path, st.Program = true, job.pid, job.path, job.prog()
		st.Running = job.pid > 0 || job.state == "running"
	}
	return st, job, nil
}

// checkLaunchd is check 8: a loaded job must be the one on disk.
func (s *launchdService) checkLaunchd(ctx context.Context, v *verified) error {
	st, job, err := s.readState(ctx, v.label)
	if err != nil {
		return err
	}
	if err := v.sameJob(job); err != nil {
		return err
	}
	v.state = st
	return nil
}

// sameJob refuses when launchd runs a different job by this label.
func (v *verified) sameJob(job launchdJob) error {
	if !job.loaded {
		return nil
	}
	if job.path != v.target.PlistPath || job.prog() != v.target.Program {
		return refuse(ReasonLaunchdMismatch, "launchd has %s loaded from %s running %s, not %s running %s",
			v.label, job.path, job.prog(), v.target.PlistPath, v.target.Program)
	}
	return nil
}
