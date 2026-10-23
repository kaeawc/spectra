package jvm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNMTEnabledInArgs(t *testing.T) {
	cases := []struct {
		args string
		want bool
	}{
		{"", false},
		{"-Xmx1g", false},
		{"-XX:NativeMemoryTracking=summary", true},
		{"-Xmx1g -XX:NativeMemoryTracking=detail", true},
		{"-XX:NativeMemoryTracking=off", false},
		{"-XX:NativeMemoryTracking=summary -XX:NativeMemoryTracking=off", false},
	}
	for _, c := range cases {
		if got := NMTEnabledInArgs(c.args); got != c.want {
			t.Errorf("NMTEnabledInArgs(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

func ctxFake(out string, err error, gotArgs *[]string) CtxCmdRunner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if gotArgs != nil {
			*gotArgs = append([]string{name}, args...)
		}
		return []byte(out), err
	}
}

func TestCollectNMTCommitted(t *testing.T) {
	var gotArgs []string
	cats, err := CollectNMTCommitted(context.Background(), 42, ctxFake(nmtSample, nil, &gotArgs), 0)
	if err != nil {
		t.Fatal(err)
	}
	if cats["Thread"] != 52428 || cats["Java Heap"] != 262144 {
		t.Fatalf("categories = %v", cats)
	}
	if strings.Join(gotArgs, " ") != "jcmd 42 VM.native_memory summary" {
		t.Fatalf("command = %v", gotArgs)
	}
}

func TestCollectNMTCommittedDisabledAndError(t *testing.T) {
	off := ctxFake("42:\nNative memory tracking is not enabled\n", nil, nil)
	if _, err := CollectNMTCommitted(context.Background(), 42, off, 0); !errors.Is(err, ErrNMTDisabled) {
		t.Fatalf("err = %v, want ErrNMTDisabled", err)
	}
	boom := errors.New("attach failed")
	if _, err := CollectNMTCommitted(context.Background(), 42, ctxFake("", boom, nil), 0); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
}

// The runner blocks until its context is cancelled, as exec.CommandContext
// does by killing the child; the probe must cancel it on timeout.
func TestCollectNMTCommittedTimeoutCancelsChild(t *testing.T) {
	cancelled := make(chan struct{})
	hang := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}
	_, err := CollectNMTCommitted(context.Background(), 42, hang, 20*time.Millisecond)
	if !errors.Is(err, ErrNMTSampleTimeout) {
		t.Fatalf("err = %v, want ErrNMTSampleTimeout", err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("runner context was not cancelled")
	}
}

func TestAdaptRunnerAbandonsOnTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	hang := func(string, ...string) ([]byte, error) {
		<-release
		return nil, nil
	}
	_, err := CollectNMTCommitted(context.Background(), 42, adaptRunner(hang), 20*time.Millisecond)
	if !errors.Is(err, ErrNMTSampleTimeout) {
		t.Fatalf("err = %v, want ErrNMTSampleTimeout", err)
	}
}

// fakeJVMRunner answers the jcmd/jstat calls collectOne makes and records
// whether VM.native_memory was invoked.
func fakeJVMRunner(vmArgs string, nmtCalls *int) CmdRunner {
	return func(name string, args ...string) ([]byte, error) {
		if name == "jcmd" && len(args) >= 2 {
			switch args[1] {
			case "VM.command_line":
				return []byte("VM Arguments:\njvm_args: " + vmArgs + "\n"), nil
			case "VM.native_memory":
				*nmtCalls++
				return []byte(nmtSample), nil
			}
		}
		return nil, errors.New("unsupported")
	}
}

func TestCollectOneSamplesNMTOnlyWhenOptedInAndEnabled(t *testing.T) {
	const nmtArgs = "-Xmx1g -XX:NativeMemoryTracking=summary"
	run := func(args string, calls *int) CmdRunner { return fakeJVMRunner(args, calls) }

	var calls int
	opts := CollectOptions{SampleNMT: true}
	opts.CmdRunner = run(nmtArgs, &calls)
	info := collectOne(context.Background(), 7, "app.Main", opts.CmdRunner, opts)
	if calls != 1 || info.NMTCommittedKiB["Thread"] != 52428 {
		t.Fatalf("opted in: calls=%d nmt=%v", calls, info.NMTCommittedKiB)
	}

	// NMT enabled in args but the caller did not opt in: never probed.
	calls = 0
	opts = CollectOptions{CmdRunner: run(nmtArgs, &calls)}
	info = collectOne(context.Background(), 7, "app.Main", opts.CmdRunner, opts)
	if calls != 0 || info.NMTCommittedKiB != nil {
		t.Fatalf("not opted in: calls=%d nmt=%v", calls, info.NMTCommittedKiB)
	}

	// Opted in but the JVM has no NMT flag: not probed.
	calls = 0
	opts = CollectOptions{SampleNMT: true, CmdRunner: run("-Xmx1g", &calls)}
	info = collectOne(context.Background(), 7, "app.Main", opts.CmdRunner, opts)
	if calls != 0 || info.NMTCommittedKiB != nil {
		t.Fatalf("flag absent: calls=%d nmt=%v", calls, info.NMTCommittedKiB)
	}
}
