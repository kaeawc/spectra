package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseFanTargets(t *testing.T) {
	targets, err := parseFanTargets("work-mac, 127.0.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}
	if targets[0].Name != "work-mac" || targets[0].Target.Address != "work-mac:7878" {
		t.Fatalf("target[0] = %+v", targets[0])
	}
	if targets[1].Name != "127.0.0.1:9000" || targets[1].Target.Address != "127.0.0.1:9000" {
		t.Fatalf("target[1] = %+v", targets[1])
	}
}

func TestParseFanTargetsRejectsEmptyList(t *testing.T) {
	if _, err := parseFanTargets(" , "); err == nil {
		t.Fatal("parseFanTargets succeeded, want error")
	}
}

func TestRunFanWithTypedShortcut(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runFanWith(
		[]string{"--hosts", "mac-a,mac-b", "jvm"},
		&stdout,
		&stderr,
		func(target connectTarget, timeout time.Duration, method string, params json.RawMessage) (json.RawMessage, error) {
			if timeout != 3*time.Second {
				return nil, fmt.Errorf("timeout = %s, want 3s", timeout)
			}
			if method != "jvm.list" {
				return nil, fmt.Errorf("method = %q, want jvm.list", method)
			}
			if params != nil {
				return nil, fmt.Errorf("params = %s, want nil", string(params))
			}
			result, _ := json.Marshal(map[string]string{"address": target.Address})
			return result, nil
		},
		func(context.Context) ([]fanTarget, error) {
			t.Fatal("discover should not be used when --hosts provided")
			return nil, nil
		},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	var out fanOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Method != "jvm.list" {
		t.Fatalf("method = %q, want jvm.list", out.Method)
	}
	if len(out.Targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(out.Targets))
	}
	if out.Targets[0].Target != "mac-a" || !out.Targets[0].OK {
		t.Fatalf("target[0] = %+v", out.Targets[0])
	}
	if out.Targets[1].Address != "mac-b:7878" || !out.Targets[1].OK {
		t.Fatalf("target[1] = %+v", out.Targets[1])
	}
}

func TestRunFanWithPartialFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runFanWith(
		[]string{"--hosts", "mac-a,mac-b", "host"},
		&stdout,
		&stderr,
		func(target connectTarget, _ time.Duration, _ string, _ json.RawMessage) (json.RawMessage, error) {
			if strings.HasPrefix(target.Address, "mac-b:") {
				return nil, errors.New("dial refused")
			}
			return json.RawMessage(`{"ok":true}`), nil
		},
		func(context.Context) ([]fanTarget, error) {
			t.Fatal("discover should not be used when --hosts provided")
			return nil, nil
		},
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}

	var out fanOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(out.Targets))
	}
	if !out.Targets[0].OK {
		t.Fatalf("target[0] = %+v", out.Targets[0])
	}
	if out.Targets[1].OK || out.Targets[1].Error != "dial refused" {
		t.Fatalf("target[1] = %+v", out.Targets[1])
	}
}

func TestRunFanWithBadShortcut(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runFanWith(
		[]string{"--hosts", "mac-a", "jvm-gc"},
		&stdout,
		&stderr,
		func(connectTarget, time.Duration, string, json.RawMessage) (json.RawMessage, error) {
			t.Fatal("caller should not run")
			return nil, nil
		},
		func(context.Context) ([]fanTarget, error) {
			t.Fatal("discover should not be used when --hosts provided")
			return nil, nil
		},
	)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "requires <pid>") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFanWithDiscoveredHosts(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runFanWith(
		[]string{"host"},
		&stdout,
		&stderr,
		func(target connectTarget, _ time.Duration, _ string, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"address": target.Address})
		},
		func(_ context.Context) ([]fanTarget, error) {
			return []fanTarget{
				{Name: "work-mac", Target: connectTarget{Network: "tcp", Address: "work-mac:7878"}},
				{Name: "alice-laptop", Target: connectTarget{Network: "tcp", Address: "alice-laptop:7878"}},
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}

	var out fanOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(out.Targets))
	}
	targetNames := map[string]bool{}
	for _, result := range out.Targets {
		targetNames[result.Target] = result.OK
	}
	if !targetNames["work-mac"] || !targetNames["alice-laptop"] {
		t.Fatalf("targets = %+v", targetNames)
	}
}

func TestRunFanWithDiscoveredHostsError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runFanWith(
		[]string{"host"},
		&stdout,
		&stderr,
		func(connectTarget, time.Duration, string, json.RawMessage) (json.RawMessage, error) {
			t.Fatal("caller should not run")
			return nil, nil
		},
		func(context.Context) ([]fanTarget, error) {
			return nil, nil
		},
	)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "requires --hosts") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
