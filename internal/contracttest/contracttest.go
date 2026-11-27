// Package contracttest writes and checks the parity cases the Rust port is
// held to (docs/design/rust-port.md, "Parity proof"). A case is a directory
// under contract/cases/<kind>/<name>/ holding its inputs (case.json, and
// optionally calls.json for subprocess replay, env.json, and a root/ tree for
// filesystem reads) and what the Go build produced (expected.stdout,
// expected.exit). Go tests write cases with -contract-update and otherwise
// fail when a checked-in case no longer matches the Go build; the Rust crate
// spectra-contract replays the same inputs and must print the same bytes.
package contracttest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/kaeawc/spectra/internal/fsutil"
	"github.com/kaeawc/spectra/internal/proc"
)

// Update rewrites the cases a test produces instead of checking them.
var Update = flag.Bool("contract-update", false, "rewrite contract/cases from the Go build")

// CasesDir is contract/cases at the repository root.
func CasesDir(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "contract", "cases")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("contracttest: no go.mod above the working directory")
		}
		dir = parent
	}
}

// Case is one case directory.
type Case struct {
	Dir string
}

// New returns contract/cases/<kind>/<name>.
func New(t testing.TB, kind, name string) Case {
	t.Helper()
	return Case{Dir: filepath.Join(CasesDir(t), kind, name)}
}

// Input writes (or checks) case.json from v.
func (c Case) Input(t testing.TB, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	c.file(t, "case.json", append(b, '\n'))
}

// Expect writes (or checks) what the Go build printed and how it exited.
func (c Case) Expect(t testing.TB, stdout []byte, exit int) {
	t.Helper()
	c.file(t, "expected.stdout", stdout)
	c.file(t, "expected.exit", []byte(strconv.Itoa(exit)+"\n"))
}

func (c Case) file(t testing.TB, name string, content []byte) {
	t.Helper()
	path := filepath.Join(c.Dir, name)
	if *Update {
		if err := os.MkdirAll(c.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := fsutil.WriteFileAtomic(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (write it with -contract-update)", path, err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("%s no longer matches the Go build; rewrite it with -contract-update and port the change to Rust", path)
	}
}

// Call is one recorded subprocess call and its result. Output that is not
// UTF-8 is stored base64-encoded in the *_b64 fields.
type Call struct {
	Name      string   `json:"name"`
	Args      []string `json:"args"`
	Stdout    string   `json:"stdout,omitempty"`
	StdoutB64 string   `json:"stdout_b64,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	StderrB64 string   `json:"stderr_b64,omitempty"`
	ExitCode  int      `json:"exit_code"`
	Error     string   `json:"error,omitempty"`
}

func textOrB64(b []byte) (string, string) {
	if utf8.Valid(b) {
		return string(b), ""
	}
	return "", base64.StdEncoding.EncodeToString(b)
}

func fromTextOrB64(text, b64 string) ([]byte, error) {
	if b64 != "" {
		return base64.StdEncoding.DecodeString(b64)
	}
	return []byte(text), nil
}

// Runner replays calls.json: each call answers the command with exactly its
// name and arguments. A case without calls.json gets a runner that fails every
// call.
func (c Case) Runner(t testing.TB) *proc.Fake {
	t.Helper()
	fake := proc.NewFake()
	raw, err := os.ReadFile(filepath.Join(c.Dir, "calls.json"))
	if errors.Is(err, os.ErrNotExist) {
		return fake
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []Call
	if err := json.Unmarshal(raw, &calls); err != nil {
		t.Fatalf("calls.json: %v", err)
	}
	for _, call := range calls {
		stdout, err := fromTextOrB64(call.Stdout, call.StdoutB64)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := fromTextOrB64(call.Stderr, call.StderrB64)
		if err != nil {
			t.Fatal(err)
		}
		resp := proc.Response{Result: proc.Result{Stdout: stdout, Stderr: stderr, ExitCode: call.ExitCode}}
		if call.Error != "" {
			resp.Err = errors.New(call.Error)
		}
		fake.OnExact(call.Name, call.Args, resp)
	}
	return fake
}

// Recorder runs commands through Inner and keeps every call, so a case can be
// recorded once on a real host and replayed everywhere.
type Recorder struct {
	Inner proc.Runner
	mu    sync.Mutex
	calls []Call
}

// Run implements proc.Runner.
func (r *Recorder) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	res, err := r.Inner.Run(ctx, cmd)
	call := Call{Name: cmd.Name, Args: append([]string{}, cmd.Args...), ExitCode: res.ExitCode}
	call.Stdout, call.StdoutB64 = textOrB64(res.Stdout)
	call.Stderr, call.StderrB64 = textOrB64(res.Stderr)
	if err != nil {
		call.Error = err.Error()
	}
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	return res, err
}

// Save writes (or checks) the recorded calls as calls.json.
func (r *Recorder) Save(t testing.TB, c Case) {
	t.Helper()
	r.mu.Lock()
	calls := append([]Call(nil), r.calls...)
	r.mu.Unlock()
	b, err := json.MarshalIndent(calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	c.file(t, "calls.json", append(b, '\n'))
}
