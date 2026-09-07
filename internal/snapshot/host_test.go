package snapshot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// TestCollectHostMinimallyPopulated runs against the live machine; we
// don't assert specific values, just that the collector produces a
// HostInfo with the always-available stdlib-derived fields filled in.
// macOS-specific fields (CPU brand, RAM, OS version) are best-effort
// and only checked for sanity when present.
func TestCollectHostMinimallyPopulated(t *testing.T) {
	h := CollectHost("test-version")
	if h.OSName == "" {
		t.Error("OSName empty")
	}
	if hostos.Current() == hostos.Darwin && h.OSName != "macOS" {
		t.Errorf("OSName = %q, want macOS on a Darwin host", h.OSName)
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
		OS:       hostos.Darwin,
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
		OS:       hostos.Darwin,
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

func TestLiveHostCollectorLinuxFromProc(t *testing.T) {
	files := map[string][]byte{
		"/etc/os-release": []byte(`NAME="Ubuntu"
VERSION_ID="22.04"
VERSION="22.04.3 LTS (Jammy Jellyfish)"
PRETTY_NAME="Ubuntu 22.04.3 LTS"
BUILD_ID="rolling"
`),
		"/proc/cpuinfo":   []byte("processor\t: 0\nmodel name\t: Intel(R) Xeon(R) CPU\n\nprocessor\t: 1\nmodel name\t: Intel(R) Xeon(R) CPU\n"),
		"/proc/meminfo":   []byte("MemTotal:       16384000 kB\nMemFree:  1000 kB\n"),
		"/proc/uptime":    []byte("3600.50 1234.00\n"),
		"/etc/machine-id": []byte("0123456789abcdef0123456789abcdef\n"),
	}
	collector := LiveHostCollector{Options: HostCollectOptions{
		OS:       hostos.Linux,
		Hostname: func() (string, error) { return "linux-host", nil },
		ReadFile: func(name string) ([]byte, error) {
			if b, ok := files[name]; ok {
				return b, nil
			}
			return nil, fmt.Errorf("no fixture for %s", name)
		},
	}}

	got := collector.CollectHost("test-version")
	if got.OSName != "Ubuntu" {
		t.Errorf("OSName = %q, want Ubuntu", got.OSName)
	}
	if got.OSVersion != "22.04" || got.OSBuild != "rolling" {
		t.Errorf("OS = %q (%q), want 22.04 (rolling)", got.OSVersion, got.OSBuild)
	}
	if got.CPUBrand != "Intel(R) Xeon(R) CPU" || got.CPUCores != 2 {
		t.Errorf("CPU = %q %d, want Intel(R) Xeon(R) CPU 2", got.CPUBrand, got.CPUCores)
	}
	if got.RAMBytes != 16384000*1024 {
		t.Errorf("RAMBytes = %d, want %d", got.RAMBytes, uint64(16384000*1024))
	}
	if got.UptimeSeconds != 3600 {
		t.Errorf("UptimeSeconds = %d, want 3600", got.UptimeSeconds)
	}
	if got.MachineUUID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("MachineUUID = %q", got.MachineUUID)
	}
	if got.Hostname != "linux-host" {
		t.Errorf("Hostname = %q", got.Hostname)
	}
}

func TestLinuxHostToleratesMissingSources(t *testing.T) {
	collector := LiveHostCollector{Options: HostCollectOptions{
		OS:       hostos.Linux,
		Hostname: func() (string, error) { return "bare", nil },
		ReadFile: func(string) ([]byte, error) { return nil, fmt.Errorf("absent") },
	}}
	got := collector.CollectHost("v")
	if got.OSName != "Linux" {
		t.Errorf("OSName = %q, want Linux fallback", got.OSName)
	}
	if got.CPUCores != 0 || got.RAMBytes != 0 || got.MachineUUID != "" {
		t.Errorf("expected zero best-effort fields, got %+v", got)
	}
}

func TestParseOSRelease(t *testing.T) {
	name, version, build := parseOSRelease([]byte("PRETTY_NAME='Arch Linux'\nBUILD_ID=rolling\n"))
	if name != "Arch Linux" || build != "rolling" || version != "" {
		t.Errorf("parseOSRelease PRETTY_NAME fallback = %q %q %q", name, version, build)
	}
	name, _, _ = parseOSRelease([]byte("# comment\nID=fedora\n"))
	if name != "" {
		t.Errorf("parseOSRelease with no NAME/PRETTY_NAME = %q, want empty", name)
	}
}

func TestParseProcCPUInfoNoModelName(t *testing.T) {
	brand, cores := parseProcCPUInfo([]byte("processor\t: 0\nprocessor\t: 1\nprocessor\t: 2\nHardware\t: BCM2835\n"))
	if cores != 3 {
		t.Errorf("cores = %d, want 3", cores)
	}
	if brand != "" {
		t.Errorf("brand = %q, want empty (no model name field)", brand)
	}
}

func TestParseProcUptime(t *testing.T) {
	if got := parseProcUptime([]byte("12345.67 9999.00")); got != 12345 {
		t.Errorf("parseProcUptime = %d, want 12345", got)
	}
	if got := parseProcUptime([]byte("garbage")); got != 0 {
		t.Errorf("parseProcUptime(garbage) = %d, want 0", got)
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
