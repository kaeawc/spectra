package helper

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kaeawc/spectra/internal/startup"
)

// run executes the verified plan. It never runs an argv outside v.planned;
// it may leave a planned bootstrap out when the job is already loaded from
// the bound plist.
func (s *launchdService) run(ctx context.Context, res *LaunchdChangeResult, v *verified) {
	if v.action == "disable" {
		s.runDisable(ctx, res, v)
	} else {
		s.runEnable(ctx, res, v)
	}
	if res.Outcome == "" {
		s.reread(ctx, res, v)
	}
}

// exec runs one planned argv and records it.
func (s *launchdService) exec(ctx context.Context, res *LaunchdChangeResult, argv []string) (startup.Command, error) {
	out, err := s.runCmd(ctx, argv[0], argv[1:]...)
	cmd := startup.Command{Argv: append([]string{}, argv...), ExitCode: out.ExitCode, Stderr: strings.TrimSpace(string(out.Stderr))}
	res.Commands = append(res.Commands, cmd)
	return cmd, err
}

func (s *launchdService) fail(res *LaunchdChangeResult, msg string, suggested ...string) {
	res.Outcome, res.Message = OutcomeError, msg
	res.Suggested = suggested
}

func (s *launchdService) runDisable(ctx context.Context, res *LaunchdChangeResult, v *verified) {
	cmd, err := s.exec(ctx, res, v.planned[0])
	if err != nil || cmd.ExitCode != 0 {
		s.fail(res, fmt.Sprintf("launchctl disable failed (exit %d): %s%s", cmd.ExitCode, cmd.Stderr, errText(err)))
		s.reread(ctx, res, v)
		res.Outcome = OutcomeError
		return
	}
	if len(v.planned) < 2 {
		return
	}
	cmd, err = s.exec(ctx, res, v.planned[1])
	switch {
	case err != nil:
		s.fail(res, "disabled but still running: launchctl bootout could not run"+errText(err), "sudo launchctl bootout system/"+v.label, "or reboot")
	case cmd.ExitCode == 0:
	case cmd.ExitCode == 3 || cmd.ExitCode == 113 || strings.Contains(cmd.Stderr, "No such process") || strings.Contains(cmd.Stderr, "Could not find specified service"):
		res.Message = "the job was no longer loaded when bootout ran"
	case cmd.ExitCode == 36 || strings.Contains(cmd.Stderr, "Operation now in progress"):
		res.Pending = true
		res.Message = "bootout is in progress: launchd is still stopping the job, so it may still show as loaded"
	default:
		s.fail(res, fmt.Sprintf("disabled but still running: launchctl bootout exited %d: %s", cmd.ExitCode, cmd.Stderr), "sudo launchctl bootout system/"+v.label, "or reboot")
	}
	if res.Outcome == OutcomeError {
		s.reread(ctx, res, v)
		res.Outcome = OutcomeError
	}
}

func (s *launchdService) runEnable(ctx context.Context, res *LaunchdChangeResult, v *verified) {
	cmd, err := s.exec(ctx, res, v.planned[0])
	if err != nil || cmd.ExitCode != 0 {
		s.fail(res, fmt.Sprintf("launchctl enable failed (exit %d): %s%s", cmd.ExitCode, cmd.Stderr, errText(err)))
		return
	}
	if len(v.planned) < 2 {
		return
	}
	// Something may have loaded the job between the checks and now.
	job, err := s.readJob(ctx, v.label)
	if err != nil {
		s.fail(res, "enabled but not loaded: could not re-read launchd before bootstrap: "+err.Error(), "sudo launchctl bootstrap system "+v.target.PlistPath, "or reboot")
		return
	}
	if job.loaded {
		if err := v.sameJob(job); err != nil {
			res.Outcome, res.Reason, res.Mismatch = OutcomeMismatch, ReasonLaunchdMismatch, refusalMessage(err)
			return
		}
		res.Skipped = append(res.Skipped, v.planned[1])
		res.Message = "the job was already loaded from the verified plist; bootstrap skipped"
		return
	}
	cmd, err = s.exec(ctx, res, v.planned[1])
	if err != nil || cmd.ExitCode != 0 {
		s.fail(res, fmt.Sprintf("enabled but not loaded: launchctl bootstrap exited %d: %s%s", cmd.ExitCode, cmd.Stderr, errText(err)),
			"sudo launchctl bootstrap system "+v.target.PlistPath, "or reboot")
		s.reread(ctx, res, v)
		res.Outcome = OutcomeError
	}
}

// reread fills After and Mismatch from launchd's state after the change.
func (s *launchdService) reread(ctx context.Context, res *LaunchdChangeResult, v *verified) {
	st, job, err := s.readState(ctx, v.label)
	if err != nil {
		if res.Outcome == "" {
			res.Outcome = OutcomeError
		}
		res.Mismatch = "could not re-read launchd after the change: " + err.Error()
		return
	}
	res.After = &st
	res.Mismatch = afterMismatch(v, res, st, job)
	switch {
	case res.Outcome != "":
	case res.Mismatch != "":
		res.Outcome = OutcomeMismatch
	default:
		res.Outcome = OutcomeOK
	}
}

func afterMismatch(v *verified, res *LaunchdChangeResult, st LaunchdState, job launchdJob) string {
	if v.action == "disable" {
		switch {
		case !st.Disabled:
			return "launchctl still reports the daemon enabled"
		case st.Loaded && len(v.planned) < 2:
			return "disabled but running: the daemon was loaded after the change was prepared; it stops at the next reboot or with sudo launchctl bootout system/" + v.label
		case st.Loaded && !res.Pending:
			return "the daemon is still loaded"
		}
		return ""
	}
	switch {
	case st.Disabled:
		return "launchctl still reports the daemon disabled"
	case !st.Loaded:
		return "the daemon is not loaded"
	}
	if err := v.sameJob(job); err != nil {
		return refusalMessage(err)
	}
	return ""
}

func refusalMessage(err error) string {
	var ref *Refusal
	if errors.As(err, &ref) {
		return ref.Message
	}
	return err.Error()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}
