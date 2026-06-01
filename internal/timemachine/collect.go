package timemachine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/plistread"
)

// CommandRunner runs a subprocess and returns combined output.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// CommandRunnerFunc adapts a function into a CommandRunner.
type CommandRunnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Run implements CommandRunner.
func (f CommandRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Options configures Time Machine collection.
type Options struct {
	Runner    CommandRunner
	Now       func() time.Time
	ReadPrefs func() (plistread.TMPrefs, error)
}

// Collect gathers read-only Time Machine state.
func Collect(ctx context.Context) (TimeMachineState, error) {
	return CollectWithOptions(ctx, Options{})
}

// CollectWithOptions gathers read-only Time Machine state with test hooks.
func CollectWithOptions(ctx context.Context, opts Options) (TimeMachineState, error) {
	run := opts.Runner
	if run == nil {
		run = execRunner{}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	readPrefs := opts.ReadPrefs
	if readPrefs == nil {
		readPrefs = plistread.ReadTimeMachinePrefs
	}

	state := TimeMachineState{
		CollectedAt:    now().UTC(),
		Destinations:   []TMDestination{},
		LocalSnapshots: []TMLocalSnapshot{},
	}
	if err := readDestinations(ctx, run, &state); err != nil {
		return state, err
	}
	if err := readStatus(ctx, run, &state); err != nil {
		return state, err
	}
	if err := readLocalSnapshots(ctx, run, &state); err != nil {
		return state, err
	}
	loaded, err := launchctlLoaded(ctx, run, "system/com.apple.backupd-auto")
	if err != nil {
		return state, err
	}
	state.SchedulerLoaded = loaded
	prefs, err := readPrefs()
	if err != nil {
		if isFullDiskAccessErr(err) {
			return state, err
		}
		return state, nil
	}
	state.AutoBackupEnabled = prefs.AutoBackup
	return state, nil
}

func readDestinations(ctx context.Context, run CommandRunner, state *TimeMachineState) error {
	out, err := run.Run(ctx, "/usr/bin/tmutil", "destinationinfo", "-X")
	if err != nil {
		if isEmptyTMOutput(out) {
			return nil
		}
		if isPermissionOutput(out, err) {
			return fmt.Errorf("tmutil destinationinfo: %w", ErrNeedsFullDiskAccess)
		}
		return fmt.Errorf("tmutil destinationinfo: %w", err)
	}
	destinations, err := ParseDestinations(out)
	if err != nil {
		return fmt.Errorf("parse tmutil destinationinfo: %w", err)
	}
	state.Destinations = destinations
	return nil
}

func readStatus(ctx context.Context, run CommandRunner, state *TimeMachineState) error {
	out, err := run.Run(ctx, "/usr/bin/tmutil", "status", "-X")
	if err != nil {
		if isPermissionOutput(out, err) {
			return fmt.Errorf("tmutil status: %w", ErrNeedsFullDiskAccess)
		}
		return nil
	}
	status, err := ParseStatus(out)
	if err != nil {
		return fmt.Errorf("parse tmutil status: %w", err)
	}
	state.Status = status
	return nil
}

func readLocalSnapshots(ctx context.Context, run CommandRunner, state *TimeMachineState) error {
	out, err := run.Run(ctx, "/usr/bin/tmutil", "listlocalsnapshots", "/", "-X")
	if err != nil {
		if isEmptyTMOutput(out) {
			return nil
		}
		if isPermissionOutput(out, err) {
			return fmt.Errorf("tmutil listlocalsnapshots: %w", ErrNeedsFullDiskAccess)
		}
		return nil
	}
	snapshots, err := ParseLocalSnapshots(out)
	if err != nil {
		return fmt.Errorf("parse tmutil listlocalsnapshots: %w", err)
	}
	state.LocalSnapshots = snapshots
	return nil
}

func launchctlLoaded(ctx context.Context, run CommandRunner, service string) (bool, error) {
	out, err := run.Run(ctx, "/bin/launchctl", "print", service)
	if err == nil {
		return true, nil
	}
	if isPermissionOutput(out, err) {
		return false, fmt.Errorf("launchctl print %s: %w", service, ErrNeedsFullDiskAccess)
	}
	text := strings.ToLower(string(out))
	if strings.Contains(text, "could not find service") ||
		strings.Contains(text, "no such process") ||
		strings.Contains(text, "bad request") {
		return false, nil
	}
	return false, nil
}

func isEmptyTMOutput(out []byte) bool {
	text := strings.ToLower(string(out))
	return len(bytes.TrimSpace(out)) == 0 ||
		strings.Contains(text, "no destinations configured") ||
		strings.Contains(text, "no local snapshots")
}

func isPermissionOutput(out []byte, err error) bool {
	if errors.Is(err, plistread.ErrNeedsFullDiskAccess) {
		return true
	}
	text := strings.ToLower(string(out))
	return strings.Contains(text, "operation not permitted") ||
		strings.Contains(text, "full disk access") ||
		strings.Contains(text, "permission denied")
}
