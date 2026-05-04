package sysinfo

import (
	"errors"
	"strings"
	"testing"
)

func stubSysctl(responses map[string]string) CmdRunner {
	return func(name string, args ...string) ([]byte, error) {
		if name != "sysctl" || len(args) < 2 {
			return nil, errors.New("unexpected command")
		}
		key := args[1]
		if v, ok := responses[key]; ok {
			return []byte(v + "\n"), nil
		}
		return nil, errors.New("no such key")
	}
}

func TestCollectSysctls(t *testing.T) {
	stub := stubSysctl(map[string]string{
		"kern.maxfiles":        "49152",
		"hw.ncpu":              "16",
		"vm.memory_pressure":   "0",
	})
	got := CollectSysctls(stub)

	if got["kern.maxfiles"] != "49152" {
		t.Errorf("kern.maxfiles = %q", got["kern.maxfiles"])
	}
	if got["hw.ncpu"] != "16" {
		t.Errorf("hw.ncpu = %q", got["hw.ncpu"])
	}
	// Keys not in the response should be absent.
	if _, ok := got["kern.boottime"]; ok {
		t.Error("kern.boottime should be absent (not in stub)")
	}
}

func TestCollectSysctlsAllFail(t *testing.T) {
	stub := func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("permission denied")
	}
	got := CollectSysctls(stub)
	if len(got) != 0 {
		t.Errorf("expected empty map on all-fail, got %d entries", len(got))
	}
}

func TestCollectSysctlsAllowlist(t *testing.T) {
	// Every allowed key should be queried; no others.
	queried := map[string]int{}
	stub := func(name string, args ...string) ([]byte, error) {
		if len(args) >= 2 {
			queried[args[1]]++
		}
		return []byte("0\n"), nil
	}
	CollectSysctls(stub)

	for _, key := range AllowedSysctls {
		if queried[key] == 0 {
			t.Errorf("allowed key %q was not queried", key)
		}
	}
	if len(queried) != len(AllowedSysctls) {
		t.Errorf("queried %d keys, want %d", len(queried), len(AllowedSysctls))
	}
}

func TestCollectSysctlsTrimWhitespace(t *testing.T) {
	stub := stubSysctl(map[string]string{
		"kern.maxfiles": "  49152  ",
	})
	got := CollectSysctls(stub)
	if strings.Contains(got["kern.maxfiles"], " ") {
		t.Errorf("value has whitespace: %q", got["kern.maxfiles"])
	}
}
