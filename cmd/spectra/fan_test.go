package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/store"
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
		func(context.Context, bool, bool, bool) ([]fanTarget, error) {
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
		func(context.Context, bool, bool, bool) ([]fanTarget, error) {
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
		func(context.Context, bool, bool, bool) ([]fanTarget, error) {
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
	origProbeTarget := fanProbeTarget
	fanProbeTarget = func(context.Context, connectTarget, time.Duration) error {
		t.Fatal("probe should not run without --probe")
		return nil
	}
	t.Cleanup(func() {
		fanProbeTarget = origProbeTarget
	})
	code := runFanWith(
		[]string{"host"},
		&stdout,
		&stderr,
		func(target connectTarget, _ time.Duration, _ string, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"address": target.Address})
		},
		func(_ context.Context, probe bool, discover bool, discoverDaemons bool) ([]fanTarget, error) {
			if probe {
				t.Fatal("discover should not probe unless --probe is set")
			}
			if discoverDaemons {
				t.Fatal("discover should not receive --discover-daemons")
			}
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

func TestRunFanWithDiscoveredHostsProbe(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	origProbeTarget := fanProbeTarget
	fanProbeTarget = func(_ context.Context, target connectTarget, _ time.Duration) error {
		switch target.Address {
		case "work-mac:7878":
			return nil
		case "deadbox:7878":
			return errors.New("unreachable")
		default:
			return errors.New("unexpected target")
		}
	}
	t.Cleanup(func() {
		fanProbeTarget = origProbeTarget
	})

	code := runFanWith(
		[]string{"--probe", "host"},
		&stdout,
		&stderr,
		func(target connectTarget, _ time.Duration, _ string, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"address": target.Address})
		},
		func(_ context.Context, probe bool, discover bool, discoverDaemons bool) ([]fanTarget, error) {
			if discover {
				t.Fatal("discover should not receive --discover")
			}
			if discoverDaemons {
				t.Fatal("discover should not receive --discover-daemons")
			}
			if !probe {
				t.Fatal("discover should receive --probe")
			}
			return []fanTarget{
				{Name: "work-mac", Target: connectTarget{Network: "tcp", Address: "work-mac:7878"}},
				{Name: "deadbox", Target: connectTarget{Network: "tcp", Address: "deadbox:7878"}},
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
	for _, result := range out.Targets {
		if result.Target != "work-mac" && result.Target != "deadbox" {
			t.Fatalf("targets = %+v", out.Targets)
		}
	}
}

func TestDiscoverFanTargetsWithProbeFiltersUnreachable(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbPath := filepath.Join(home, ".spectra", "spectra.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	takenAt := time.Date(2026, time.May, 5, 12, 0, 0, 0, time.UTC)
	if err := db.SaveSnapshot(ctx, store.SnapshotInput{
		ID:          "snap-work-mac",
		MachineUUID: "uuid-work-mac",
		TakenAt:     takenAt,
		Kind:        "live",
		Hostname:    "work-mac",
		OSName:      "macOS",
		OSVersion:   "15.0",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSnapshot(ctx, store.SnapshotInput{
		ID:          "snap-deadbox",
		MachineUUID: "uuid-deadbox",
		TakenAt:     takenAt,
		Kind:        "live",
		Hostname:    "deadbox",
		OSName:      "macOS",
		OSVersion:   "15.0",
	}); err != nil {
		t.Fatal(err)
	}

	var probes int
	origProbeTarget := fanProbeTarget
	fanProbeTarget = func(_ context.Context, target connectTarget, _ time.Duration) error {
		probes++
		if target.Address == "work-mac:7878" {
			return nil
		}
		if target.Address == "deadbox:7878" {
			return errors.New("unreachable")
		}
		return nil
	}
	t.Cleanup(func() { fanProbeTarget = origProbeTarget })

	targets, err := discoverFanTargets(ctx, true, false, false)
	if err != nil {
		t.Fatalf("discoverFanTargets = %v", err)
	}
	if probes != 2 {
		t.Fatalf("probes = %d, want 2", probes)
	}
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	if targets[0].Name != "work-mac" || targets[0].Target.Address != "work-mac:7878" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestRunFanWithDiscoverFanout(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotDiscover bool
	code := runFanWith(
		[]string{"--discover", "host"},
		&stdout,
		&stderr,
		func(target connectTarget, _ time.Duration, _ string, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"address": target.Address})
		},
		func(_ context.Context, probe bool, discover bool, discoverDaemons bool) ([]fanTarget, error) {
			gotDiscover = discover
			if probe {
				t.Fatal("discover should not receive --probe")
			}
			if discoverDaemons {
				t.Fatal("discover should not receive --discover-daemons")
			}
			if !discover {
				t.Fatal("discover should receive --discover")
			}
			return []fanTarget{
				{Name: "work-mac", Target: connectTarget{Network: "tcp", Address: "work-mac:7878"}},
				{Name: "alice-laptop", Target: connectTarget{Network: "tcp", Address: "alice-laptop:7878"}},
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !gotDiscover {
		t.Fatalf("discover flag was not passed")
	}

	var out fanOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(out.Targets))
	}
}

func TestRunFanWithDiscoverDaemons(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotDiscoverDaemons bool
	code := runFanWith(
		[]string{"--discover-daemons", "host"},
		&stdout,
		&stderr,
		func(target connectTarget, _ time.Duration, _ string, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal(map[string]string{"address": target.Address})
		},
		func(_ context.Context, probe bool, discover bool, discoverDaemons bool) ([]fanTarget, error) {
			if probe || discover {
				t.Fatalf("probe=%t discover=%t, want false false", probe, discover)
			}
			gotDiscoverDaemons = discoverDaemons
			return []fanTarget{
				{Name: "work-mac", Target: connectTarget{Network: "tcp", Address: "work-mac:7878"}},
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !gotDiscoverDaemons {
		t.Fatal("discover-daemons flag was not passed")
	}
	var out fanOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 1 || out.Targets[0].Target != "work-mac" {
		t.Fatalf("targets = %+v", out.Targets)
	}
}

func TestDiscoverFanTargetsWithDiscoveredPeers(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbPath := filepath.Join(home, ".spectra", "spectra.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	takenAt := time.Date(2026, time.May, 5, 12, 0, 0, 0, time.UTC)
	if err := db.SaveSnapshot(ctx, store.SnapshotInput{
		ID:          "snap-work-mac",
		MachineUUID: "uuid-work-mac",
		TakenAt:     takenAt,
		Kind:        "live",
		Hostname:    "work-mac",
		OSName:      "macOS",
		OSVersion:   "15.0",
	}); err != nil {
		t.Fatal(err)
	}

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return []string{"work-mac", "alice-laptop"}, nil
	}
	t.Cleanup(func() { fanDiscoverPeers = origDiscoverPeers })

	targets, err := discoverFanTargets(ctx, false, true, false)
	if err != nil {
		t.Fatalf("discoverFanTargets = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}
	if targets[0].Name != "alice-laptop" || targets[0].Target.Address != "alice-laptop:7878" {
		t.Fatalf("targets[0] = %+v", targets[0])
	}
	if targets[1].Name != "work-mac" || targets[1].Target.Address != "work-mac:7878" {
		t.Fatalf("targets[1] = %+v", targets[1])
	}
}

func TestDiscoverFanTargetsWithDiscoveredDaemons(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return []string{"work-mac", "deadbox", "work-mac"}, nil
	}
	origProbeTarget := fanProbeTarget
	fanProbeTarget = func(_ context.Context, target connectTarget, _ time.Duration) error {
		if target.Address == "work-mac:7878" {
			return nil
		}
		return errors.New("unreachable")
	}
	t.Cleanup(func() {
		fanDiscoverPeers = origDiscoverPeers
		fanProbeTarget = origProbeTarget
	})

	targets, err := discoverFanTargets(ctx, false, false, true)
	if err != nil {
		t.Fatalf("discoverFanTargets = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	if targets[0].Name != "work-mac" || targets[0].Target.Address != "work-mac:7878" {
		t.Fatalf("target = %+v", targets[0])
	}
}

func TestDiscoverFanTargetsWithDiscoveryFallbackToPeers(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return []string{"remote-laptop", "remote-laptop"}, nil
	}
	t.Cleanup(func() { fanDiscoverPeers = origDiscoverPeers })

	targets, err := discoverFanTargets(ctx, false, true, false)
	if err != nil {
		t.Fatalf("discoverFanTargets = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	if targets[0].Name != "remote-laptop" || targets[0].Target.Address != "remote-laptop:7878" {
		t.Fatalf("targets[0] = %+v", targets[0])
	}
}

func TestDiscoverFanTargetsDiscoveryErrorWithDbFallback(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbPath := filepath.Join(home, ".spectra", "spectra.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	takenAt := time.Date(2026, time.May, 5, 12, 0, 0, 0, time.UTC)
	if err := db.SaveSnapshot(ctx, store.SnapshotInput{
		ID:          "snap-work-mac",
		MachineUUID: "uuid-work-mac",
		TakenAt:     takenAt,
		Kind:        "live",
		Hostname:    "work-mac",
		OSName:      "macOS",
		OSVersion:   "15.0",
	}); err != nil {
		t.Fatal(err)
	}

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return nil, errors.New("tailscale unavailable")
	}
	t.Cleanup(func() { fanDiscoverPeers = origDiscoverPeers })

	targets, err := discoverFanTargets(ctx, false, true, false)
	if err != nil {
		t.Fatalf("discoverFanTargets = %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "work-mac" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestDiscoverFanTargetsDiscoveryErrorWithoutDb(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	origDiscoverPeers := fanDiscoverPeers
	fanDiscoverPeers = func() ([]string, error) {
		return nil, errors.New("tailscale unavailable")
	}
	t.Cleanup(func() { fanDiscoverPeers = origDiscoverPeers })

	_, err := discoverFanTargets(ctx, false, true, false)
	if err == nil {
		t.Fatal("discoverFanTargets succeeded, want error")
	}
	if !strings.Contains(err.Error(), "discover remote hosts") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverTailscalePeersFromStatusJSON(t *testing.T) {
	origRunTailscaleStatus := runTailscaleStatus
	runTailscaleStatus = func() ([]byte, error) {
		body := map[string]any{
			"Peers": map[string]map[string]string{
				"foo": {"DNSName": "foo.tailnet.example"},
				"bar": {"HostName": "bar-laptop"},
				"dup": {"DNSName": "foo.tailnet.example"},
			},
			"Peer": map[string]map[string]string{
				"legacy": {"DNSName": "legacy.tailnet.example"},
			},
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return raw, nil
	}
	t.Cleanup(func() { runTailscaleStatus = origRunTailscaleStatus })

	peers, err := discoverTailscalePeers()
	if err != nil {
		t.Fatalf("discoverTailscalePeers = %v", err)
	}
	if len(peers) != 3 {
		t.Fatalf("len(peers) = %d, want 3", len(peers))
	}
	expected := []string{"bar-laptop", "foo.tailnet.example", "legacy.tailnet.example"}
	for i, peer := range peers {
		if peer != expected[i] {
			t.Fatalf("peers[%d] = %q, want %q", i, peer, expected[i])
		}
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
		func(context.Context, bool, bool, bool) ([]fanTarget, error) {
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
