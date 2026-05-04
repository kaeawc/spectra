// Package proc provides a small abstraction over os/exec so production
// code can take a dependency on Runner instead of calling exec.Command
// directly. Tests substitute Fake to script command output and assert
// on the call shape without spawning real subprocesses.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Cmd describes a command invocation. Zero values are sane defaults:
// no working directory override, no extra environment, empty stdin.
type Cmd struct {
	// Name is the executable to run (e.g. "git").
	Name string
	// Args are the arguments passed to Name.
	Args []string
	// Dir, if non-empty, sets the subprocess's working directory.
	Dir string
	// Env, if non-nil, replaces the subprocess's environment. nil
	// inherits the parent process's environment.
	Env []string
	// Stdin, if non-empty, is written to the subprocess's stdin.
	Stdin []byte
}

// Result captures the outcome of running a Cmd. ExitCode is 0 on
// success, non-zero on failure, and -1 when the process could not be
// started or was killed before reporting status (e.g. context cancel).
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Runner runs commands. Implementations must be safe for concurrent use.
type Runner interface {
	// Run executes cmd and returns the result. Run returns an error
	// only when the command could not be invoked at all (e.g. binary
	// not found, context canceled). A non-zero ExitCode is reported
	// via Result, not error, so callers can decide whether non-zero
	// is a failure for their use case.
	Run(ctx context.Context, cmd Cmd) (Result, error)
}

// OS is a Runner backed by os/exec.
type OS struct{}

// Run implements Runner using exec.CommandContext.
func (OS) Run(ctx context.Context, cmd Cmd) (Result, error) {
	// #nosec G204 -- proc.Runner is the abstraction for running caller-supplied subprocess commands
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.Env != nil {
		c.Env = cmd.Env
	}
	if len(cmd.Stdin) > 0 {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	res := Result{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}
	if c.ProcessState != nil {
		res.ExitCode = c.ProcessState.ExitCode()
	} else {
		// Start failed (binary not found, fork failure, etc.) so
		// we never got a process to ask. Convention: -1.
		res.ExitCode = -1
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// A non-zero exit is reflected in ExitCode; not an
			// error from the runner's perspective.
			return res, nil
		}
		return res, err
	}
	return res, nil
}

// Default is a process-wide OS runner for callers that cannot reach a
// composition root. Prefer injecting a Runner explicitly.
var Default Runner = OS{}

// Call records a single invocation observed by Fake.
type Call struct {
	Name string
	Args []string
	Dir  string
}

// Response describes a scripted reply Fake should return for a matched
// call. Err, when non-nil, is returned as the second return from Run;
// otherwise Result is returned with err == nil.
type Response struct {
	Result Result
	Err    error
}

// Matcher decides whether a Cmd should produce a given Response.
type Matcher func(Cmd) bool

// MatchExact returns a Matcher that requires Name and Args (in order)
// to equal the given values. Dir and Env are not compared.
func MatchExact(name string, args ...string) Matcher {
	want := append([]string{}, args...)
	return func(c Cmd) bool {
		if c.Name != name {
			return false
		}
		if len(c.Args) != len(want) {
			return false
		}
		for i, a := range want {
			if c.Args[i] != a {
				return false
			}
		}
		return true
	}
}

// MatchPrefix returns a Matcher that requires Name to equal name and
// the first len(args) entries of Cmd.Args to equal args. Useful for
// matching "git diff ..." regardless of trailing arguments.
func MatchPrefix(name string, args ...string) Matcher {
	prefix := append([]string{}, args...)
	return func(c Cmd) bool {
		if c.Name != name {
			return false
		}
		if len(c.Args) < len(prefix) {
			return false
		}
		for i, a := range prefix {
			if c.Args[i] != a {
				return false
			}
		}
		return true
	}
}

type rule struct {
	match Matcher
	resp  Response
}

// Fake is a Runner that replays scripted responses based on Matcher
// rules and records every call it observes. Fake is safe for concurrent
// use.
//
// When no rule matches an incoming Cmd, Run returns an error describing
// the unexpected call. This makes tests fail loudly when the code under
// test invokes commands the test did not anticipate.
type Fake struct {
	mu    sync.Mutex
	rules []rule
	calls []Call
}

// NewFake returns an empty Fake. Use On to register responses.
func NewFake() *Fake { return &Fake{} }

// On registers resp as the response to commands matching m. Rules are
// evaluated in registration order; the first match wins.
func (f *Fake) On(m Matcher, resp Response) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{match: m, resp: resp})
	return f
}

// OnExact is a convenience for On(MatchExact(name, args...), resp).
func (f *Fake) OnExact(name string, args []string, resp Response) *Fake {
	return f.On(MatchExact(name, args...), resp)
}

// Run records the call and returns the first matching response, or an
// error if no rule matches.
func (f *Fake) Run(_ context.Context, cmd Cmd) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Name: cmd.Name,
		Args: append([]string{}, cmd.Args...),
		Dir:  cmd.Dir,
	})
	rules := f.rules
	f.mu.Unlock()

	for _, r := range rules {
		if r.match(cmd) {
			return r.resp.Result, r.resp.Err
		}
	}
	return Result{ExitCode: -1}, fmt.Errorf("proc.Fake: no rule matched %s", formatCmd(cmd))
}

// Calls returns a copy of every call observed so far, in invocation order.
// Args slices are also copied so callers can mutate the result safely.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	for i, c := range f.calls {
		out[i] = Call{
			Name: c.Name,
			Args: append([]string{}, c.Args...),
			Dir:  c.Dir,
		}
	}
	return out
}

// CallCount returns how many calls Fake has observed.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// Reset clears observed calls. Registered rules are kept.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func formatCmd(c Cmd) string {
	if len(c.Args) == 0 {
		return c.Name
	}
	return c.Name + " " + strings.Join(c.Args, " ")
}
