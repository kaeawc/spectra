package jvm

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultNMTSampleTimeout bounds the jcmd VM.native_memory summary call made
// while sampling. A hung attach must not stall the whole collection.
const DefaultNMTSampleTimeout = 5 * time.Second

// ErrNMTSampleTimeout means the bounded NMT summary call did not return in time.
var ErrNMTSampleTimeout = errors.New("native memory summary timed out")

// NMTEnabledInArgs reports whether the JVM was started with
// -XX:NativeMemoryTracking=summary|detail. The last occurrence wins, matching
// HotSpot flag processing. Sampling is gated on this so NMT is never enabled
// (or probed) implicitly on JVMs that did not opt in.
func NMTEnabledInArgs(vmArgs string) bool {
	const prefix = "-XX:NativeMemoryTracking="
	enabled := false
	for _, tok := range strings.Fields(vmArgs) {
		if v, ok := strings.CutPrefix(tok, prefix); ok {
			enabled = v == "summary" || v == "detail"
		}
	}
	return enabled
}

// CtxCmdRunner is a CmdRunner whose child process is killed when ctx ends.
type CtxCmdRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCtxRunner runs the real command and kills it when ctx is done.
func DefaultCtxRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// ctxRunnerFor picks the runner used for the NMT probe: an explicit
// context-aware runner wins; an injected plain CmdRunner is adapted (it can
// be abandoned on timeout but not killed); otherwise the real, killable one.
func ctxRunnerFor(opts CollectOptions) CtxCmdRunner {
	switch {
	case opts.CmdRunnerContext != nil:
		return opts.CmdRunnerContext
	case opts.CmdRunner != nil:
		return adaptRunner(opts.CmdRunner)
	}
	return DefaultCtxRunner
}

func adaptRunner(run CmdRunner) CtxCmdRunner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		type result struct {
			out []byte
			err error
		}
		ch := make(chan result, 1)
		go func() {
			out, err := run(name, args...)
			ch <- result{out, err}
		}()
		select {
		case r := <-ch:
			return r.out, r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// CollectNMTCommitted runs a cost-bounded `jcmd <pid> VM.native_memory summary`
// and returns committed KiB per category. The jcmd child is killed (when run
// honors ctx) once timeout elapses or ctx ends; zero timeout means
// DefaultNMTSampleTimeout. It returns ErrNMTDisabled when the target reports
// NMT is off and ErrNMTSampleTimeout when the call exceeds the timeout.
func CollectNMTCommitted(ctx context.Context, pid int, run CtxCmdRunner, timeout time.Duration) (map[string]int64, error) {
	if run == nil {
		run = DefaultCtxRunner
	}
	if timeout <= 0 {
		timeout = DefaultNMTSampleTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run(ctx, "jcmd", strconv.Itoa(pid), "VM.native_memory", "summary")
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("pid %d: %w after %s", pid, ErrNMTSampleTimeout, timeout)
		}
		return nil, fmt.Errorf("jcmd VM.native_memory summary: %w", err)
	}
	b := ParseNMTSummary(string(out))
	if !b.Enabled {
		return nil, ErrNMTDisabled
	}
	if len(b.Categories) == 0 {
		return nil, nil
	}
	cats := make(map[string]int64, len(b.Categories))
	for _, c := range b.Categories {
		cats[c.Name] = c.CommittedKiB
	}
	return cats, nil
}
