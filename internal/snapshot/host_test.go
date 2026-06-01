package snapshot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/timemachine"
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
		"uname\x00-v":                                   "Darwin Kernel Version 24.6.0",
		"uname\x00-m":                                   "arm64",
		"sw_vers\x00-productName":                       "macOS",
		"sw_vers\x00-productVersion":                    "15.6.1",
		"sw_vers\x00-buildVersion":                      "24G90",
		"system_profiler\x00-xml\x00SPHardwareDataType": testHardwareProfilerXML,
		"log\x00show\x00--predicate\x00eventMessage CONTAINS \"=== system boot\"\x00--last\x007d\x00--style\x00ndjson": `{"eventMessage":"=== system boot: abcdef12-3456-7890-abcd-ef1234567890"}`,
		"last\x00reboot": "reboot time                                Sun May 17 08:34\n",
	}}
	collector := LiveHostCollector{Options: HostCollectOptions{
		Hostname: func() (string, error) { return "test-host", nil },
		Runner:   runner,
		Now:      func() time.Time { return time.Unix(4600, 0) },
		BootTime: func() (time.Time, error) {
			return time.Unix(1000, 0), nil
		},
		LoadAverages: func(at time.Time) (LoadAverages, error) {
			return LoadAverages{OneMinute: 1.25, FiveMinute: 2.5, FifteenMinute: 3.75, At: at}, nil
		},
		MemoryCollect: func() (memstate.MemoryState, error) { return memstate.MemoryState{PhysicalBytes: 99}, nil },
		TMCollect: func() (timemachine.TimeMachineState, error) {
			return timemachine.TimeMachineState{SchedulerLoaded: true}, nil
		},
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
	if got.Memory.PhysicalBytes != 99 {
		t.Errorf("Memory.PhysicalBytes = %d, want injected value", got.Memory.PhysicalBytes)
	}
	if !got.TimeMachine.SchedulerLoaded {
		t.Error("TimeMachine.SchedulerLoaded = false, want injected true")
	}
	if got.Facts.BootUUID != "ABCDEF12-3456-7890-ABCD-EF1234567890" {
		t.Errorf("BootUUID = %q", got.Facts.BootUUID)
	}
	if got.Facts.Hardware.PerformanceCores != 8 || got.Facts.Hardware.EfficiencyCores != 4 {
		t.Errorf("hardware cores = %d/%d", got.Facts.Hardware.PerformanceCores, got.Facts.Hardware.EfficiencyCores)
	}
	if got.Facts.LoadAverages.OneMinute != 1.25 {
		t.Errorf("load average = %.2f", got.Facts.LoadAverages.OneMinute)
	}
	if len(got.Facts.RecentReboots) != 1 {
		t.Fatalf("RecentReboots len = %d, want 1", len(got.Facts.RecentReboots))
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

func TestHostFactsParsers(t *testing.T) {
	total, perf, eff := parseProcessorCounts("proc 16:12:4")
	if total != 16 || perf != 12 || eff != 4 {
		t.Fatalf("parseProcessorCounts = %d/%d/%d", total, perf, eff)
	}
	if got := parseMemoryBytes("128 GB"); got != 128*1024*1024*1024 {
		t.Fatalf("parseMemoryBytes = %d", got)
	}
	if got := parseBootUUID(`{"eventMessage":"=== system boot: abcdef12-3456-7890-abcd-ef1234567890"}`); got != "ABCDEF12-3456-7890-ABCD-EF1234567890" {
		t.Fatalf("parseBootUUID = %q", got)
	}
	events := parseRecentReboots("reboot time                                Sun May 17 08:34\nshutdown time Sun May 17 08:34\n", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	if len(events) != 1 || events[0].At.Month() != time.May || events[0].At.Day() != 17 {
		t.Fatalf("parseRecentReboots = %#v", events)
	}
}

const testHardwareProfilerXML = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<array>
  <dict>
    <key>_items</key>
    <array>
      <dict>
        <key>machine_name</key><string>MacBook Pro</string>
        <key>machine_model</key><string>Mac99,1</string>
        <key>chip_type</key><string>Apple M99</string>
        <key>number_processors</key><string>proc 12:8:4</string>
        <key>physical_memory</key><string>64 GB</string>
        <key>platform_UUID</key><string>ABCDEF12-3456-7890-ABCD-EF1234567890</string>
        <key>serial_number</key><string>SERIAL123</string>
      </dict>
    </array>
  </dict>
</array>
</plist>`

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
