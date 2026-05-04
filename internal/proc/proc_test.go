package proc

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestOS_Run_StdoutCaptured(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX echo")
	}
	t.Parallel()

	res, err := OS{}.Run(context.Background(), Cmd{
		Name: "sh",
		Args: []string{"-c", "echo hello"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "hello" {
		t.Fatalf("Stdout = %q, want %q", got, "hello")
	}
}

func TestOS_Run_NonZeroExitNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	t.Parallel()

	res, err := OS{}.Run(context.Background(), Cmd{
		Name: "sh",
		Args: []string{"-c", "exit 7"},
	})
	if err != nil {
		t.Fatalf("expected nil error for non-zero exit, got %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}
}

func TestOS_Run_StdinPassed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX cat")
	}
	t.Parallel()

	res, err := OS{}.Run(context.Background(), Cmd{
		Name:  "cat",
		Stdin: []byte("piped"),
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if string(res.Stdout) != "piped" {
		t.Fatalf("Stdout = %q, want %q", string(res.Stdout), "piped")
	}
}

func TestOS_Run_DirHonored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX pwd")
	}
	t.Parallel()

	dir := t.TempDir()
	res, err := OS{}.Run(context.Background(), Cmd{
		Name: "sh",
		Args: []string{"-c", "pwd"},
		Dir:  dir,
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	got := strings.TrimSpace(string(res.Stdout))
	if !strings.HasSuffix(got, dir) {
		t.Fatalf("pwd = %q, want suffix %q", got, dir)
	}
}

func TestOS_Run_BinaryNotFoundReturnsError(t *testing.T) {
	t.Parallel()

	_, err := OS{}.Run(context.Background(), Cmd{Name: "definitely-not-a-real-binary-xyz"})
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
}

func TestOS_Run_ContextCancelReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX sleep")
	}
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := OS{}.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "sleep 5"}})
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
}

func TestFake_OnExactReturnsScriptedResponse(t *testing.T) {
	t.Parallel()

	f := NewFake().OnExact("git", []string{"rev-parse", "HEAD"}, Response{
		Result: Result{Stdout: []byte("deadbeef\n"), ExitCode: 0},
	})

	res, err := f.Run(context.Background(), Cmd{Name: "git", Args: []string{"rev-parse", "HEAD"}})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if string(res.Stdout) != "deadbeef\n" {
		t.Fatalf("Stdout = %q", string(res.Stdout))
	}
}

func TestFake_FirstMatchWins(t *testing.T) {
	t.Parallel()

	f := NewFake().
		On(MatchPrefix("git", "diff"), Response{Result: Result{Stdout: []byte("first")}}).
		On(MatchPrefix("git", "diff"), Response{Result: Result{Stdout: []byte("second")}})

	res, _ := f.Run(context.Background(), Cmd{Name: "git", Args: []string{"diff", "--name-only"}})
	if string(res.Stdout) != "first" {
		t.Fatalf("Stdout = %q, want first", string(res.Stdout))
	}
}

func TestFake_NoMatchIsAnError(t *testing.T) {
	t.Parallel()

	f := NewFake()
	_, err := f.Run(context.Background(), Cmd{Name: "git", Args: []string{"status"}})
	if err == nil {
		t.Fatal("expected error for unmatched call")
	}
	if !strings.Contains(err.Error(), "git status") {
		t.Fatalf("error %q does not mention the unmatched call", err.Error())
	}
}

func TestFake_PropagatesScriptedError(t *testing.T) {
	t.Parallel()

	scripted := errors.New("boom")
	f := NewFake().OnExact("git", []string{"fetch"}, Response{Err: scripted})

	_, err := f.Run(context.Background(), Cmd{Name: "git", Args: []string{"fetch"}})
	if !errors.Is(err, scripted) {
		t.Fatalf("err = %v, want %v", err, scripted)
	}
}

func TestFake_RecordsCallsInOrder(t *testing.T) {
	t.Parallel()

	f := NewFake().
		On(MatchPrefix("git"), Response{Result: Result{Stdout: []byte("ok")}}).
		On(MatchPrefix("go"), Response{Result: Result{Stdout: []byte("ok")}})

	_, _ = f.Run(context.Background(), Cmd{Name: "git", Args: []string{"status"}, Dir: "/tmp"})
	_, _ = f.Run(context.Background(), Cmd{Name: "go", Args: []string{"build"}})

	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("Calls len = %d, want 2", len(calls))
	}
	if calls[0].Name != "git" || calls[0].Dir != "/tmp" {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	if calls[1].Name != "go" || calls[1].Args[0] != "build" {
		t.Errorf("calls[1] = %+v", calls[1])
	}
}

func TestFake_CallsCopyIsIndependent(t *testing.T) {
	t.Parallel()

	f := NewFake().On(MatchPrefix("git"), Response{Result: Result{Stdout: []byte("ok")}})
	_, _ = f.Run(context.Background(), Cmd{Name: "git", Args: []string{"status"}})

	calls := f.Calls()
	calls[0].Args[0] = "MUTATED"
	again := f.Calls()
	if again[0].Args[0] != "status" {
		t.Fatalf("Calls() returned a shared slice; mutation leaked: %+v", again[0])
	}
}

func TestFake_Reset(t *testing.T) {
	t.Parallel()

	f := NewFake().On(MatchPrefix("git"), Response{Result: Result{Stdout: []byte("ok")}})
	_, _ = f.Run(context.Background(), Cmd{Name: "git", Args: []string{"status"}})

	f.Reset()
	if got := f.CallCount(); got != 0 {
		t.Fatalf("CallCount after Reset = %d, want 0", got)
	}
	// Rules survive Reset.
	if _, err := f.Run(context.Background(), Cmd{Name: "git", Args: []string{"status"}}); err != nil {
		t.Fatalf("Run after Reset error: %v", err)
	}
}

func TestFake_ConcurrentRun(t *testing.T) {
	t.Parallel()

	f := NewFake().On(MatchPrefix("git"), Response{Result: Result{Stdout: []byte("ok")}})

	var wg sync.WaitGroup
	const goroutines = 16
	const iterations = 50
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, _ = f.Run(context.Background(), Cmd{Name: "git", Args: []string{"status"}})
			}
		}()
	}
	wg.Wait()

	if got := f.CallCount(); got != goroutines*iterations {
		t.Fatalf("CallCount = %d, want %d", got, goroutines*iterations)
	}
}

func TestMatchExact_RejectsArgCountMismatch(t *testing.T) {
	t.Parallel()

	m := MatchExact("git", "status")
	if m(Cmd{Name: "git", Args: []string{"status", "--porcelain"}}) {
		t.Fatal("MatchExact should not match when extra args are passed")
	}
}

func TestMatchPrefix_AllowsTrailingArgs(t *testing.T) {
	t.Parallel()

	m := MatchPrefix("git", "diff")
	if !m(Cmd{Name: "git", Args: []string{"diff", "--name-only", "main"}}) {
		t.Fatal("MatchPrefix should match with trailing args")
	}
	if m(Cmd{Name: "git", Args: []string{"status"}}) {
		t.Fatal("MatchPrefix should not match a different subcommand")
	}
	if m(Cmd{Name: "git"}) {
		t.Fatal("MatchPrefix should not match when prefix is longer than args")
	}
}

// Compile-time assertion that OS and *Fake satisfy Runner.
var (
	_ Runner = OS{}
	_ Runner = (*Fake)(nil)
)
