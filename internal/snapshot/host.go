// Package snapshot defines the structured system inventory produced by
// each Spectra collection run. See docs/design/system-inventory.md for
// the full data model.
//
// Today the package only ships the HostInfo collector and Snapshot
// assembly. Process, JVM, JDK, toolchain, and other collectors land
// alongside the daemon work.
package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// CommandRunner runs a system command and returns stdout.
type CommandRunner interface {
	Run(name string, args ...string) (string, error)
}

// HostCollector gathers HostInfo for one machine.
type HostCollector interface {
	CollectHost(spectraVersion string) HostInfo
}

// HostCollectOptions configures host collection.
type HostCollectOptions struct {
	Hostname func() (string, error)
	Runner   CommandRunner
	Now      func() time.Time
	// OS selects the collection backend. The zero value
	// (hostos.Unknown) resolves to the host OS via hostos.Resolve.
	OS hostos.Kind
	// ReadFile reads a file's bytes; defaults to os.ReadFile. The Linux
	// backend uses it for /etc/os-release, /proc, and the machine-id
	// files; injectable so tests can serve fixtures instead of the live
	// filesystem.
	ReadFile func(string) ([]byte, error)
}

// LiveHostCollector gathers HostInfo from the current machine.
type LiveHostCollector struct {
	Options HostCollectOptions
}

type execCommandRunner struct{}

func (execCommandRunner) Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// HostInfo is one host's identifying and capacity facts at snapshot
// time. Every field is best-effort; any collector failure leaves the
// field empty / zero rather than failing the snapshot.
type HostInfo struct {
	Hostname       string `json:"hostname"`
	MachineUUID    string `json:"machine_uuid,omitempty"`
	OSName         string `json:"os_name"`            // "macOS"
	OSVersion      string `json:"os_version"`         // "15.6.1"
	OSBuild        string `json:"os_build,omitempty"` // "24G90"
	CPUBrand       string `json:"cpu_brand,omitempty"`
	CPUCores       int    `json:"cpu_cores"`
	RAMBytes       uint64 `json:"ram_bytes"`
	Architecture   string `json:"architecture"` // arm64 | amd64
	UptimeSeconds  int64  `json:"uptime_seconds"`
	SpectraVersion string `json:"spectra_version,omitempty"`
}

// CollectHost gathers HostInfo from the local machine. Spectra version
// is provided by the caller because it lives in main and isn't reliably
// readable from a library package.
func CollectHost(spectraVersion string) HostInfo {
	return LiveHostCollector{}.CollectHost(spectraVersion)
}

func (c LiveHostCollector) CollectHost(spectraVersion string) HostInfo {
	hostname := c.Options.Hostname
	if hostname == nil {
		hostname = os.Hostname
	}

	h := HostInfo{
		Architecture:   runtime.GOARCH,
		SpectraVersion: spectraVersion,
	}
	if name, err := hostname(); err == nil {
		h.Hostname = name
	}

	switch hostos.Resolve(c.Options.OS) {
	case hostos.Darwin:
		c.collectDarwin(&h)
	case hostos.Linux:
		c.collectLinux(&h)
	default:
		h.OSName = runtime.GOOS
	}
	return h
}

// collectDarwin fills macOS-specific fields via sw_vers/sysctl/ioreg.
func (c LiveHostCollector) collectDarwin(h *HostInfo) {
	runner := c.Options.Runner
	if runner == nil {
		runner = execCommandRunner{}
	}
	now := c.Options.Now
	if now == nil {
		now = time.Now
	}

	h.OSName = "macOS"
	if v, err := runner.Run("sw_vers", "-productVersion"); err == nil {
		h.OSVersion = v
	}
	if v, err := runner.Run("sw_vers", "-buildVersion"); err == nil {
		h.OSBuild = v
	}
	if v, err := runner.Run("sysctl", "-n", "machdep.cpu.brand_string"); err == nil {
		h.CPUBrand = v
	}
	if v, err := runner.Run("sysctl", "-n", "hw.ncpu"); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			h.CPUCores = n
		}
	}
	if v, err := runner.Run("sysctl", "-n", "hw.memsize"); err == nil {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			h.RAMBytes = n
		}
	}
	if uptime := readUptime(runner, now); uptime > 0 {
		h.UptimeSeconds = uptime
	}
	if uuid := readMachineUUID(runner); uuid != "" {
		h.MachineUUID = uuid
	}
}

// collectLinux fills fields from /etc/os-release and /proc. Every read is
// best-effort: a missing or unreadable source leaves its field zero
// rather than failing the snapshot.
func (c LiveHostCollector) collectLinux(h *HostInfo) {
	readFile := c.Options.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}

	h.OSName = "Linux"
	if b, err := readFile("/etc/os-release"); err == nil {
		if name, version, build := parseOSRelease(b); name != "" {
			h.OSName = name
			h.OSVersion = version
			h.OSBuild = build
		}
	}
	if b, err := readFile("/proc/cpuinfo"); err == nil {
		if brand, cores := parseProcCPUInfo(b); brand != "" || cores > 0 {
			h.CPUBrand = brand
			h.CPUCores = cores
		}
	}
	if b, err := readFile("/proc/meminfo"); err == nil {
		if ram := parseProcMeminfo(b); ram > 0 {
			h.RAMBytes = ram
		}
	}
	if b, err := readFile("/proc/uptime"); err == nil {
		if up := parseProcUptime(b); up > 0 {
			h.UptimeSeconds = up
		}
	}
	if id := readMachineID(readFile); id != "" {
		h.MachineUUID = id
	}
}

// parseOSRelease extracts the distro name, version, and build id from
// /etc/os-release content. It prefers NAME for the name, VERSION_ID for
// the version, and BUILD_ID for the build (all commonly present; any may
// be empty). Values may be single- or double-quoted.
func parseOSRelease(data []byte) (name, version, build string) {
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(k)] = unquoteOSRelease(strings.TrimSpace(v))
	}
	name = fields["NAME"]
	if name == "" {
		name = fields["PRETTY_NAME"]
	}
	return name, fields["VERSION_ID"], fields["BUILD_ID"]
}

func unquoteOSRelease(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// parseProcCPUInfo returns the CPU model name and the logical-core count
// from /proc/cpuinfo. Cores is the number of "processor" entries, which
// matches macOS's hw.ncpu (logical CPUs). On architectures without a
// "model name" field (e.g. arm64) the brand may be empty.
func parseProcCPUInfo(data []byte) (brand string, cores int) {
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "processor":
			cores++
		case "model name", "Model", "cpu model":
			if brand == "" {
				brand = val
			}
		}
	}
	return brand, cores
}

// parseProcMeminfo returns total RAM in bytes from the MemTotal line of
// /proc/meminfo, which is reported in kibibytes.
func parseProcMeminfo(data []byte) uint64 {
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "MemTotal" {
			continue
		}
		fields := strings.Fields(val)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// parseProcUptime returns whole seconds since boot from /proc/uptime,
// whose first field is uptime in seconds as a float.
func parseProcUptime(data []byte) int64 {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0
	}
	return int64(secs)
}

// readMachineID returns a stable per-machine identifier from
// /etc/machine-id, falling back to the D-Bus machine-id. Both survive
// reboots (unlike /proc/sys/kernel/random/boot_id).
func readMachineID(readFile func(string) ([]byte, error)) string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := readFile(p); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}

// readUptime parses kern.boottime and returns seconds since boot.
// `sysctl -n kern.boottime` prints "{ sec = 1234567890, usec = 0 } Mon ...".
func readUptime(runner CommandRunner, now func() time.Time) int64 {
	out, err := runner.Run("sysctl", "-n", "kern.boottime")
	if err != nil {
		return 0
	}
	re := regexp.MustCompile(`sec\s*=\s*(\d+)`)
	m := re.FindStringSubmatch(out)
	if len(m) < 2 {
		return 0
	}
	boot, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0
	}
	current := now().Unix()
	if current < boot {
		return 0
	}
	return current - boot
}

// readMachineUUID parses IOPlatformUUID out of `ioreg`. Stable per
// machine; survives reinstalls of macOS.
func readMachineUUID(runner CommandRunner) string {
	out, err := runner.Run("ioreg", "-d2", "-c", "IOPlatformExpertDevice")
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([A-F0-9-]+)"`)
	m := re.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// String renders HostInfo as a brief multi-line summary suitable for the
// CLI's plain output. JSON callers should marshal directly.
func (h HostInfo) String() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "host:           %s\n", h.Hostname)
	if h.MachineUUID != "" {
		fmt.Fprintf(&b, "machine-uuid:   %s\n", h.MachineUUID)
	}
	fmt.Fprintf(&b, "os:             %s %s", h.OSName, h.OSVersion)
	if h.OSBuild != "" {
		fmt.Fprintf(&b, " (%s)", h.OSBuild)
	}
	fmt.Fprintln(&b)
	if h.CPUBrand != "" {
		fmt.Fprintf(&b, "cpu:            %s (%d cores, %s)\n", h.CPUBrand, h.CPUCores, h.Architecture)
	}
	if h.RAMBytes > 0 {
		fmt.Fprintf(&b, "ram:            %s\n", humanBytes(h.RAMBytes))
	}
	if h.UptimeSeconds > 0 {
		fmt.Fprintf(&b, "uptime:         %s\n", humanDuration(h.UptimeSeconds))
	}
	if h.SpectraVersion != "" {
		fmt.Fprintf(&b, "spectra:        %s\n", h.SpectraVersion)
	}
	return b.String()
}

func humanBytes(n uint64) string {
	const k = 1024
	switch {
	case n >= k*k*k*k:
		return fmt.Sprintf("%.1f TB", float64(n)/float64(k*k*k*k))
	case n >= k*k*k:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(k*k))
	case n >= k:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(k))
	}
	return fmt.Sprintf("%d B", n)
}

func humanDuration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	days := int64(d / (24 * time.Hour))
	hours := int64((d % (24 * time.Hour)) / time.Hour)
	mins := int64((d % time.Hour) / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
