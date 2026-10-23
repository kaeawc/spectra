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

func TestCollectNMTCommitted(t *testing.T) {
	var gotArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(nmtSample), nil
	}
	cats, err := CollectNMTCommitted(42, run, 0)
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
	off := func(string, ...string) ([]byte, error) {
		return []byte("42:\nNative memory tracking is not enabled\n"), nil
	}
	if _, err := CollectNMTCommitted(42, off, 0); !errors.Is(err, ErrNMTDisabled) {
		t.Fatalf("err = %v, want ErrNMTDisabled", err)
	}
	boom := errors.New("attach failed")
	fail := func(string, ...string) ([]byte, error) { return nil, boom }
	if _, err := CollectNMTCommitted(42, fail, 0); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
}

func TestCollectNMTCommittedTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	hang := func(string, ...string) ([]byte, error) {
		<-release
		return nil, nil
	}
	_, err := CollectNMTCommitted(42, hang, 20*time.Millisecond)
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

func TestCollectOneSamplesNMTOnlyWhenEnabled(t *testing.T) {
	var calls int
	info := collectOne(context.Background(), 7, "app.Main", fakeJVMRunner("-Xmx1g -XX:NativeMemoryTracking=summary", &calls), CollectOptions{})
	if calls != 1 || info.NMTCommittedKiB["Thread"] != 52428 {
		t.Fatalf("calls=%d nmt=%v", calls, info.NMTCommittedKiB)
	}

	calls = 0
	info = collectOne(context.Background(), 7, "app.Main", fakeJVMRunner("-Xmx1g", &calls), CollectOptions{})
	if calls != 0 || info.NMTCommittedKiB != nil {
		t.Fatalf("NMT must not be probed when not enabled: calls=%d nmt=%v", calls, info.NMTCommittedKiB)
	}
}
