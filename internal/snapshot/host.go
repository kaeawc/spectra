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
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/timemachine"
	"howett.net/plist"
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
	Hostname      func() (string, error)
	Runner        CommandRunner
	Now           func() time.Time
	BootTime      func() (time.Time, error)
	LoadAverages  func(time.Time) (LoadAverages, error)
	MemoryCollect func() (memstate.MemoryState, error)
	TMCollect     func() (timemachine.TimeMachineState, error)
}

// LiveHostCollector gathers HostInfo from the current machine.
type LiveHostCollector struct {
	Options HostCollectOptions
}

type execCommandRunner struct{}

func (execCommandRunner) Run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
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
	Hostname       string                       `json:"hostname"`
	MachineUUID    string                       `json:"machine_uuid,omitempty"`
	OSName         string                       `json:"os_name"`            // "macOS"
	OSVersion      string                       `json:"os_version"`         // "15.6.1"
	OSBuild        string                       `json:"os_build,omitempty"` // "24G90"
	CPUBrand       string                       `json:"cpu_brand,omitempty"`
	CPUCores       int                          `json:"cpu_cores"`
	RAMBytes       uint64                       `json:"ram_bytes"`
	Architecture   string                       `json:"architecture"` // arm64 | amd64
	UptimeSeconds  int64                        `json:"uptime_seconds"`
	SpectraVersion string                       `json:"spectra_version,omitempty"`
	Memory         memstate.MemoryState         `json:"memory,omitempty"`
	TimeMachine    timemachine.TimeMachineState `json:"time_machine,omitempty"`
	Facts          HostFacts                    `json:"-"`
}

// HostFacts is the durable host baseline for each snapshot.
type HostFacts struct {
	Hostname         string        `json:"hostname"`
	KernelVersion    string        `json:"kernel_version,omitempty"`
	Architecture     string        `json:"architecture"`
	OSProductName    string        `json:"os_product_name"`
	OSProductVersion string        `json:"os_product_version,omitempty"`
	OSBuildVersion   string        `json:"os_build_version,omitempty"`
	Hardware         Hardware      `json:"hardware"`
	BootTime         time.Time     `json:"boot_time,omitempty"`
	BootUUID         string        `json:"boot_uuid,omitempty"`
	Uptime           time.Duration `json:"uptime,omitempty"`
	LoadAverages     LoadAverages  `json:"load_averages"`
	RecentReboots    []RebootEvent `json:"recent_reboots,omitempty"`
}

type Hardware struct {
	ModelName        string `json:"model_name,omitempty"`
	ModelIdentifier  string `json:"model_identifier,omitempty"`
	Chip             string `json:"chip,omitempty"`
	SerialNumber     string `json:"serial_number,omitempty"`
	HardwareUUID     string `json:"hardware_uuid,omitempty"`
	CPUCores         int    `json:"cpu_cores,omitempty"`
	PerformanceCores int    `json:"performance_cores,omitempty"`
	EfficiencyCores  int    `json:"efficiency_cores,omitempty"`
	MemoryBytes      uint64 `json:"memory_bytes,omitempty"`
}

type LoadAverages struct {
	OneMinute     float64   `json:"one_minute,omitempty"`
	FiveMinute    float64   `json:"five_minute,omitempty"`
	FifteenMinute float64   `json:"fifteen_minute,omitempty"`
	At            time.Time `json:"at,omitempty"`
}

type RebootEvent struct {
	At                    time.Time `json:"at"`
	PreviousShutdownCause int       `json:"previous_shutdown_cause,omitempty"`
}

// CollectHost gathers HostInfo from the local machine. Spectra version
// is provided by the caller because it lives in main and isn't reliably
// readable from a library package.
func CollectHost(spectraVersion string) HostInfo {
	return LiveHostCollector{}.CollectHost(spectraVersion)
}

func (c LiveHostCollector) CollectHost(spectraVersion string) HostInfo {
	deps := hostDepsFromOptions(c.Options)
	facts := collectHostFacts(deps)

	h := HostInfo{
		Hostname:       facts.Hostname,
		MachineUUID:    facts.Hardware.HardwareUUID,
		OSName:         facts.OSProductName,
		OSVersion:      facts.OSProductVersion,
		OSBuild:        facts.OSBuildVersion,
		CPUBrand:       facts.Hardware.Chip,
		CPUCores:       facts.Hardware.CPUCores,
		RAMBytes:       facts.Hardware.MemoryBytes,
		Architecture:   facts.Architecture,
		UptimeSeconds:  int64(facts.Uptime.Seconds()),
		SpectraVersion: spectraVersion,
		Facts:          facts,
	}
	if h.OSName == "" {
		h.OSName = "macOS"
	}
	if h.Architecture == "" {
		h.Architecture = runtime.GOARCH
	}
	if h.CPUBrand == "" {
		if v, err := deps.runner.Run("sysctl", "-n", "machdep.cpu.brand_string"); err == nil {
			h.CPUBrand = v
		}
	}
	if h.CPUCores == 0 {
		if v, err := deps.runner.Run("sysctl", "-n", "hw.ncpu"); err == nil {
			if n, err := strconv.Atoi(v); err == nil {
				h.CPUCores = n
			}
		}
	}
	if h.RAMBytes == 0 {
		if v, err := deps.runner.Run("sysctl", "-n", "hw.memsize"); err == nil {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				h.RAMBytes = n
			}
		}
	}
	if h.UptimeSeconds == 0 {
		if uptime := readUptime(deps.runner, deps.now); uptime > 0 {
			h.UptimeSeconds = uptime
		}
	}
	if h.MachineUUID == "" {
		if uuid := readMachineUUID(deps.runner); uuid != "" {
			h.MachineUUID = uuid
		}
	}
	h.Memory = collectHostMemory(deps.memoryCollect)
	h.TimeMachine = collectHostTimeMachine(deps.tmCollect)
	return h
}

func collectHostFacts(deps hostDeps) HostFacts {
	now := deps.now()
	facts := HostFacts{
		OSProductName: "macOS",
		Architecture:  runtime.GOARCH,
	}
	if name, err := deps.hostname(); err == nil {
		facts.Hostname = name
	}
	populateHostFactStrings(&facts, deps.runner)
	facts.Hardware = collectHardware(deps.runner)
	populateHardwareFallbacks(&facts.Hardware, deps.runner)
	populateBootFacts(&facts, deps, now)
	if loads, err := deps.loadAverages(now); err == nil {
		facts.LoadAverages = loads
	}
	facts.RecentReboots = readRecentReboots(deps.runner, now)
	return facts
}

func populateHostFactStrings(facts *HostFacts, runner CommandRunner) {
	if v, err := runner.Run("uname", "-v"); err == nil {
		facts.KernelVersion = v
	}
	if v, err := runner.Run("uname", "-m"); err == nil {
		facts.Architecture = v
	}
	if v, err := runner.Run("sw_vers", "-productName"); err == nil {
		facts.OSProductName = v
	}
	if v, err := runner.Run("sw_vers", "-productVersion"); err == nil {
		facts.OSProductVersion = v
	}
	if v, err := runner.Run("sw_vers", "-buildVersion"); err == nil {
		facts.OSBuildVersion = v
	}
}

func populateHardwareFallbacks(hardware *Hardware, runner CommandRunner) {
	if hardware.CPUCores == 0 {
		if v, err := runner.Run("sysctl", "-n", "hw.ncpu"); err == nil {
			hardware.CPUCores, _ = strconv.Atoi(v)
		}
	}
	if hardware.MemoryBytes == 0 {
		if v, err := runner.Run("sysctl", "-n", "hw.memsize"); err == nil {
			hardware.MemoryBytes, _ = strconv.ParseUint(v, 10, 64)
		}
	}
}

func populateBootFacts(facts *HostFacts, deps hostDeps, now time.Time) {
	if boot, err := deps.bootTime(); err == nil && !boot.IsZero() {
		facts.BootTime = boot
	} else {
		facts.BootTime = readBootTimeFromSysctl(deps.runner)
	}
	if !facts.BootTime.IsZero() && now.After(facts.BootTime) {
		facts.Uptime = now.Sub(facts.BootTime).Round(time.Second)
	}
	if uuid := readBootUUID(deps.runner); uuid != "" {
		facts.BootUUID = uuid
	}
}

func collectHardware(runner CommandRunner) Hardware {
	out, err := runner.Run("system_profiler", "-xml", "SPHardwareDataType")
	if err != nil {
		return Hardware{}
	}
	item := systemProfilerHardwareItem(out)
	if len(item) == 0 {
		return Hardware{}
	}
	h := Hardware{
		ModelName:       stringValue(item["machine_name"]),
		ModelIdentifier: stringValue(item["machine_model"]),
		Chip:            stringValue(item["chip_type"]),
		SerialNumber:    stringValue(item["serial_number"]),
		HardwareUUID:    stringValue(item["platform_UUID"]),
	}
	h.CPUCores, h.PerformanceCores, h.EfficiencyCores = parseProcessorCounts(stringValue(item["number_processors"]))
	h.MemoryBytes = parseMemoryBytes(stringValue(item["physical_memory"]))
	return h
}

func systemProfilerHardwareItem(out string) map[string]any {
	var root []map[string]any
	if _, err := plist.Unmarshal([]byte(out), &root); err != nil || len(root) == 0 {
		return nil
	}
	items, ok := root[0]["_items"].([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	item, _ := items[0].(map[string]any)
	return item
}

func stringValue(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func parseProcessorCounts(raw string) (total, performance, efficiency int) {
	re := regexp.MustCompile(`proc\s+(\d+):(\d+):(\d+)`)
	m := re.FindStringSubmatch(raw)
	if len(m) != 4 {
		return 0, 0, 0
	}
	total, _ = strconv.Atoi(m[1])
	performance, _ = strconv.Atoi(m[2])
	efficiency, _ = strconv.Atoi(m[3])
	return total, performance, efficiency
}

func parseMemoryBytes(raw string) uint64 {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	unit := "B"
	if len(fields) > 1 {
		unit = strings.ToUpper(fields[1])
	}
	multiplier := float64(1)
	switch unit {
	case "KB":
		multiplier = 1024
	case "MB":
		multiplier = 1024 * 1024
	case "GB":
		multiplier = 1024 * 1024 * 1024
	case "TB":
		multiplier = 1024 * 1024 * 1024 * 1024
	}
	return uint64(value * multiplier)
}

func readBootTimeFromSysctl(runner CommandRunner) time.Time {
	out, err := runner.Run("sysctl", "-n", "kern.boottime")
	if err != nil {
		return time.Time{}
	}
	re := regexp.MustCompile(`sec\s*=\s*(\d+)`)
	m := re.FindStringSubmatch(out)
	if len(m) < 2 {
		return time.Time{}
	}
	boot, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(boot, 0)
}

func readBootUUID(runner CommandRunner) string {
	if out, err := runner.Run("sysctl", "-n", "kern.bootsessionuuid"); err == nil {
		if uuid := strings.TrimSpace(out); uuid != "" {
			return uuid
		}
	}
	if out, err := runner.Run("log", "show", "--predicate", `eventMessage CONTAINS "=== system boot"`, "--last", "7d", "--style", "ndjson"); err == nil {
		if uuid := parseBootUUID(out); uuid != "" {
			return uuid
		}
	}
	return ""
}

func parseBootUUID(out string) string {
	re := regexp.MustCompile(`=== system boot:\s*([A-Fa-f0-9-]{36})`)
	m := re.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return strings.ToUpper(m[1])
}

func readRecentReboots(runner CommandRunner, now time.Time) []RebootEvent {
	out, err := runner.Run("last", "reboot")
	if err != nil {
		return nil
	}
	return parseRecentReboots(out, now)
}

func parseRecentReboots(out string, now time.Time) []RebootEvent {
	var events []RebootEvent
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if len(events) >= 10 {
			break
		}
		at, ok := parseLastRebootLine(line, now)
		if ok {
			events = append(events, RebootEvent{At: at})
		}
	}
	return events
}

func parseLastRebootLine(line string, now time.Time) (time.Time, bool) {
	if !strings.HasPrefix(line, "reboot") {
		return time.Time{}, false
	}
	parts := strings.Fields(line)
	if len(parts) < 6 || parts[1] != "time" {
		return time.Time{}, false
	}
	raw := strings.Join(parts[2:6], " ") + fmt.Sprintf(" %d", now.Year())
	at, err := time.ParseInLocation("Mon Jan _2 15:04 2006", raw, now.Location())
	if err != nil {
		return time.Time{}, false
	}
	if at.After(now.Add(24 * time.Hour)) {
		at = at.AddDate(-1, 0, 0)
	}
	return at, true
}

func readUptime(runner CommandRunner, now func() time.Time) int64 {
	boot := readBootTimeFromSysctl(runner)
	if boot.IsZero() {
		return 0
	}
	current := now().Unix()
	if current < boot.Unix() {
		return 0
	}
	return current - boot.Unix()
}

type hostDeps struct {
	hostname      func() (string, error)
	runner        CommandRunner
	now           func() time.Time
	bootTime      func() (time.Time, error)
	loadAverages  func(time.Time) (LoadAverages, error)
	memoryCollect func() (memstate.MemoryState, error)
	tmCollect     func() (timemachine.TimeMachineState, error)
}

func hostDepsFromOptions(opts HostCollectOptions) hostDeps {
	deps := hostDeps{
		hostname:      opts.Hostname,
		runner:        opts.Runner,
		now:           opts.Now,
		bootTime:      opts.BootTime,
		loadAverages:  opts.LoadAverages,
		memoryCollect: opts.MemoryCollect,
		tmCollect:     opts.TMCollect,
	}
	if deps.hostname == nil {
		deps.hostname = os.Hostname
	}
	if deps.runner == nil {
		deps.runner = execCommandRunner{}
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.bootTime == nil {
		deps.bootTime = collectBootTime
	}
	if deps.loadAverages == nil {
		deps.loadAverages = collectLoadAverages
	}
	if deps.memoryCollect == nil {
		deps.memoryCollect = memstate.Collect
	}
	if deps.tmCollect == nil {
		deps.tmCollect = func() (timemachine.TimeMachineState, error) {
			return timemachine.Collect(context.Background())
		}
	}
	return deps
}

func collectHostMemory(collect func() (memstate.MemoryState, error)) memstate.MemoryState {
	memory, err := collect()
	if err != nil {
		return memstate.MemoryState{}
	}
	return memory
}

func collectHostTimeMachine(collect func() (timemachine.TimeMachineState, error)) timemachine.TimeMachineState {
	state, err := collect()
	if err != nil {
		return timemachine.TimeMachineState{}
	}
	return state
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
