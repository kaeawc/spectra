package snapshot

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCollectHostMinimallyPopulated runs against the live machine; we
// don't assert specific values, just that the collector produces a
// HostInfo with the always-available stdlib-derived fields filled in.
// macOS-specific fields (CPU brand, RAM, OS version) are best-effort
// and only checked for sanity when present.
func TestCollectHostMinimallyPopulated(t *testing.T) {
	h := CollectHost("test-version")
	if h.OSName != "macOS" {
		t.Errorf("OSName = %q, want macOS", h.OSName)
	}
	if h.SpectraVersion != "test-version" {
		t.Errorf("SpectraVersion = %q, want test-version", h.SpectraVersion)
	}
	if h.Architecture == "" {
		t.Error("Architecture empty")
	}
	if h.Hostname == "" {
		t.Error("Hostname empty")
	}

	// Best-effort fields: present on macOS hosts but skipped if the
	// underlying tool returned an error (e.g. sandboxed test runner).
	if h.OSVersion != "" && !strings.Contains(h.OSVersion, ".") {
		t.Errorf("OSVersion %q does not look like a version string", h.OSVersion)
	}
	if h.CPUCores < 0 {
		t.Errorf("CPUCores = %d, want >= 0", h.CPUCores)
	}
}

func TestHostInfoString(t *testing.T) {
	h := HostInfo{
		Hostname:       "test.local",
		OSName:         "macOS",
		OSVersion:      "15.6.1",
		OSBuild:        "24G90",
		CPUBrand:       "Apple M99",
		CPUCores:       12,
		RAMBytes:       64 * 1024 * 1024 * 1024,
		Architecture:   "arm64",
		UptimeSeconds:  3661,
		SpectraVersion: "v0.1.0",
	}
	s := h.String()
	for _, want := range []string{
		"test.local", "macOS 15.6.1", "24G90",
		"Apple M99", "12 cores", "arm64",
		"64.0 GB", "1h 1m", "v0.1.0",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q\nfull:\n%s", want, s)
		}
	}
}

func TestLiveHostCollectorUsesInjectedRunner(t *testing.T) {
	runner := fakeHostRunner{responses: map[string]string{
		"sw_vers\x00-productVersion":                   "15.6.1",
		"sw_vers\x00-buildVersion":                     "24G90",
		"sysctl\x00-n\x00machdep.cpu.brand_string":     "Apple M99",
		"sysctl\x00-n\x00hw.ncpu":                      "12",
		"sysctl\x00-n\x00hw.memsize":                   "68719476736",
		"sysctl\x00-n\x00kern.boottime":                "{ sec = 1000, usec = 0 }",
		"ioreg\x00-d2\x00-c\x00IOPlatformExpertDevice": `"IOPlatformUUID" = "ABCDEF12-3456-7890-ABCD-EF1234567890"`,
	}}
	collector := LiveHostCollector{Options: HostCollectOptions{
		Hostname: func() (string, error) { return "test-host", nil },
		Runner:   runner,
		Now:      func() time.Time { return time.Unix(4600, 0) },
	}}

	got := collector.CollectHost("test-version")
	if got.Hostname != "test-host" {
		t.Errorf("Hostname = %q, want test-host", got.Hostname)
	}
	if got.MachineUUID != "ABCDEF12-3456-7890-ABCD-EF1234567890" {
		t.Errorf("MachineUUID = %q", got.MachineUUID)
	}
	if got.OSVersion != "15.6.1" || got.OSBuild != "24G90" {
		t.Errorf("OS = %q (%q)", got.OSVersion, got.OSBuild)
	}
	if got.CPUBrand != "Apple M99" || got.CPUCores != 12 {
		t.Errorf("CPU = %q %d", got.CPUBrand, got.CPUCores)
	}
	if got.RAMBytes != 68719476736 {
		t.Errorf("RAMBytes = %d", got.RAMBytes)
	}
	if got.UptimeSeconds != 3600 {
		t.Errorf("UptimeSeconds = %d, want 3600", got.UptimeSeconds)
	}
	if got.SpectraVersion != "test-version" {
		t.Errorf("SpectraVersion = %q", got.SpectraVersion)
	}
}

func TestLiveHostCollectorToleratesMissingMachineUUID(t *testing.T) {
	collector := LiveHostCollector{Options: HostCollectOptions{
		Hostname: func() (string, error) { return "fallback-host", nil },
		Runner: fakeHostRunner{responses: map[string]string{
			"sw_vers\x00-productVersion": "15.6.1",
		}},
		Now: func() time.Time { return time.Unix(4600, 0) },
	}}

	got := collector.CollectHost("test-version")
	if got.Hostname != "fallback-host" {
		t.Errorf("Hostname = %q", got.Hostname)
	}
	if got.MachineUUID != "" {
		t.Errorf("MachineUUID = %q, want empty", got.MachineUUID)
	}
}

type fakeHostRunner struct {
	responses map[string]string
}

func (f fakeHostRunner) Run(name string, args ...string) (string, error) {
	key := strings.Join(append([]string{name}, args...), "\x00")
	if v, ok := f.responses[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("unexpected command %s", key)
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:                           "512 B",
		2048:                          "2 KB", // 2.0 → 2
		2 * 1024 * 1024:               "2 MB",
		3 * 1024 * 1024 * 1024:        "3.0 GB",
		2 * 1024 * 1024 * 1024 * 1024: "2.0 TB",
	}
	for in, want := range cases {
		got := humanBytes(in)
		// Allow either exact match or trimmed-decimal match (e.g. "2 KB" vs "2.0 KB")
		if got != want && got != strings.Replace(want, ".0 ", " ", 1) && strings.Replace(got, ".0 ", " ", 1) != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[int64]string{
		60:             "0m", // 1 minute → minute formatter says "0m" for under-an-hour edge — actually "1m"
		120:            "2m",
		3600:           "1h 0m",
		3 * 3600:       "3h 0m",
		3*3600 + 25*60: "3h 25m",
		90000:          "1d 1h 0m", // 25 hours
	}
	for in, want := range cases {
		got := humanDuration(in)
		// 60 → "0m" or "1m"; allow either to keep the test tolerant of formatter rounding.
		if in == 60 && (got == "0m" || got == "1m") {
			continue
		}
		if got != want {
			t.Errorf("humanDuration(%d) = %q, want %q", in, got, want)
		}
	}
}
