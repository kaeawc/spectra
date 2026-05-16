package helper

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/kaeawc/spectra/internal/bundleid"
)

// CmdRunner abstracts subprocess calls for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// allowedPowermetricsSamplers is the strict allowlist of sampler names the
// helper will pass to powermetrics. Untrusted callers may name any subset.
var allowedPowermetricsSamplers = map[string]bool{
	"tasks":      true,
	"cpu_power":  true,
	"gpu_power":  true,
	"ane_power":  true,
	"network":    true,
	"disk":       true,
	"interrupts": true,
	"thermal":    true,
}

// validatePowermetricsSamplers returns the joined sampler string when every
// element is on the allowlist. An empty list (or any unknown sampler) yields
// the empty string so the caller falls back to the helper default.
func validatePowermetricsSamplers(in []string) string {
	if len(in) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !allowedPowermetricsSamplers[s] {
			return ""
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// defaultRunner runs the real command.
func defaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// RegisterAll registers all helper methods on d using the provided runner.
// run may be nil (uses the real commands).
func RegisterAll(d *Dispatcher, run CmdRunner) {
	registerAll(d, run, nil)
}

func registerAll(d *Dispatcher, run CmdRunner, fsUsageStarter fsUsageStarter) {
	if run == nil {
		run = defaultRunner
	}
	fsUsage := newFSUsageManager(fsUsageStarter)
	netCapture := newNetCaptureManager(nil, "")

	d.Register("helper.health", func(_ uint32, _ json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "helper": true}, nil
	})

	d.Register("helper.powermetrics.sample", func(_ uint32, params json.RawMessage) (any, error) {
		var p struct {
			DurationMS int      `json:"duration_ms"`
			Samplers   []string `json:"samplers"`
		}
		_ = json.Unmarshal(params, &p)
		if p.DurationMS <= 0 {
			p.DurationMS = 500
		}
		samplers := validatePowermetricsSamplers(p.Samplers)
		if samplers == "" {
			samplers = "cpu_power,gpu_power,network,disk"
		}
		out, err := run("powermetrics",
			"--samplers", samplers,
			"-n", "1",
			"-i", fmt.Sprint(p.DurationMS),
			"--format", "plist",
		)
		if err != nil {
			return nil, fmt.Errorf("powermetrics: %w", err)
		}
		return map[string]any{"raw_plist": string(out)}, nil
	})

	d.Register("helper.process.tree", func(_ uint32, _ json.RawMessage) (any, error) {
		// ps -axwwo with full information — same as the user-space collector
		// but run as root so system daemons appear.
		out, err := run("ps", "-axwwo", "pid=,ppid=,rss=,vsz=,uid=,user=,command=")
		if err != nil {
			return nil, fmt.Errorf("ps: %w", err)
		}
		return map[string]any{"raw_ps": string(out)}, nil
	})

	d.Register("helper.firewall.rules", func(_ uint32, _ json.RawMessage) (any, error) {
		out, err := run("pfctl", "-s", "rules")
		if err != nil {
			return nil, fmt.Errorf("pfctl rules: %w", err)
		}
		return map[string]any{"raw_rules": string(out)}, nil
	})

	d.Register("helper.fs_usage.start", func(_ uint32, params json.RawMessage) (any, error) {
		var p fsUsageStartParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("helper.fs_usage.start invalid params: %w", err)
		}
		return fsUsage.start(p)
	})

	d.Register("helper.fs_usage.stop", func(_ uint32, params json.RawMessage) (any, error) {
		var p fsUsageStopParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("helper.fs_usage.stop invalid params: %w", err)
		}
		return fsUsage.stop(p)
	})

	d.Register("helper.net_capture.start", func(uid uint32, params json.RawMessage) (any, error) {
		var p netCaptureStartParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("helper.net_capture.start invalid params: %w", err)
		}
		return netCapture.start(uid, p)
	})

	d.Register("helper.net_capture.stop", func(_ uint32, params json.RawMessage) (any, error) {
		var p netCaptureStopParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("helper.net_capture.stop invalid params: %w", err)
		}
		return netCapture.stop(p)
	})

	d.Register("helper.tcc.system.query", func(_ uint32, params json.RawMessage) (any, error) {
		var p struct {
			BundleID string `json:"bundle_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.BundleID == "" {
			return nil, fmt.Errorf("helper.tcc.system.query requires {\"bundle_id\":\"...\"}")
		}
		if !bundleid.Valid(p.BundleID) {
			return nil, fmt.Errorf("helper.tcc.system.query rejects invalid bundle_id %q", p.BundleID)
		}
		const tccDB = "/Library/Application Support/com.apple.TCC/TCC.db"
		query := fmt.Sprintf(
			`SELECT service, auth_value FROM access WHERE client=%q`,
			p.BundleID,
		)
		out, err := run("sqlite3", tccDB, query)
		if err != nil {
			return nil, fmt.Errorf("tcc query: %w (is Full Disk Access granted to the helper?)", err)
		}
		return map[string]any{"raw_rows": string(out), "bundle_id": p.BundleID}, nil
	})
}
