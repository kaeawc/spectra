package jvm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/diag"
)

// DiagnosticsProvider adapts a JVM version to the runtime-neutral diag.Provider
// interface.
type DiagnosticsProvider struct {
	Version string
}

// DiagnosticsMatrix returns the diagnostic support matrix for this JVM version.
func (p DiagnosticsProvider) DiagnosticsMatrix() diag.Matrix {
	return DiagnosticsMatrix(p.Version)
}

// DiagnosticsMatrix returns the JVM diagnostic support posture for a runtime.
// It is data, not control flow: callers can show it in JSON, rules can reason
// over it, and future runtime packages can expose the same diag.Matrix shape.
func DiagnosticsMatrix(version string) diag.Matrix {
	major := parseJavaMajor(version)
	return diag.Matrix{
		Architecture: "jvm",
		Runtime:      "HotSpot-compatible JVM",
		Version:      version,
		Capabilities: []diag.Capability{
			jcmdCapability(major),
			threadPrintCapability(major),
			virtualThreadDumpCapability(major),
			virtualThreadJFRCapability(major),
			jstatCapability(major),
			jfrCapability(major),
			nmtCapability(major),
			jmxCapability(major),
		},
	}
}

func jcmdCapability(major int) diag.Capability {
	return diag.Capability{
		ID:          "jvm.jcmd",
		Category:    "tooling",
		Status:      statusSince(major, 8),
		Since:       "8",
		Command:     []string{"jcmd", "<pid>", "help"},
		Description: "HotSpot diagnostic command transport used by most live JVM inspections.",
	}
}

func threadPrintCapability(major int) diag.Capability {
	return diag.Capability{
		ID:          "jvm.thread_dump.platform",
		Category:    "threads",
		Status:      statusSince(major, 8),
		Since:       "8",
		Command:     []string{"jcmd", "<pid>", "Thread.print"},
		Description: "Traditional live thread dump for platform threads, monitors, and deadlock clues.",
		Limitations: "On virtual-thread-heavy JVMs this is not the preferred full inventory view.",
	}
}

func virtualThreadDumpCapability(major int) diag.Capability {
	status := statusSince(major, 21)
	limitations := "Requires JDK 21+ target support; output can be large for applications with many virtual threads."
	if major > 0 && major < 21 {
		limitations = "Virtual threads are not a production feature before JDK 21."
	}
	return diag.Capability{
		ID:          "jvm.thread_dump.virtual",
		Category:    "threads",
		Status:      status,
		Since:       "21",
		Command:     []string{"jcmd", "<pid>", "Thread.dump_to_file", "-format=json", "<path>"},
		Description: "Virtual-thread-aware thread inventory suitable for Loom-era diagnostics.",
		Limitations: limitations,
	}
}

func virtualThreadJFRCapability(major int) diag.Capability {
	return diag.Capability{
		ID:          "jvm.jfr.virtual_threads",
		Category:    "flight_recorder",
		Status:      statusSince(major, 21),
		Since:       "21",
		Command:     []string{"jcmd", "<pid>", "JFR.start", "settings=profile"},
		Description: "JFR virtual-thread lifecycle and pinning events for post-capture analysis.",
	}
}

func jstatCapability(major int) diag.Capability {
	return diag.Capability{
		ID:          "jvm.jstat.gc",
		Category:    "gc",
		Status:      statusSince(major, 8),
		Since:       "8",
		Command:     []string{"jstat", "-gc", "<pid>"},
		Description: "One-shot GC and memory-pool counters.",
	}
}

func jfrCapability(major int) diag.Capability {
	return diag.Capability{
		ID:          "jvm.jfr.control",
		Category:    "flight_recorder",
		Status:      statusSince(major, 11),
		Since:       "11",
		Command:     []string{"jcmd", "<pid>", "JFR.start"},
		Description: "Start, stop, dump, and summarize Java Flight Recorder recordings.",
		Limitations: "JFR exists in many JDK 8 vendor builds, but Spectra treats JDK 11+ as the portable baseline.",
	}
}

func nmtCapability(major int) diag.Capability {
	return diag.Capability{
		ID:          "jvm.native_memory",
		Category:    "memory",
		Status:      statusSince(major, 8),
		Since:       "8",
		Command:     []string{"jcmd", "<pid>", "VM.native_memory", "summary"},
		Description: "Native memory tracking category summary.",
		Limitations: "Requires the target JVM to have started with NativeMemoryTracking enabled.",
	}
}

func jmxCapability(major int) diag.Capability {
	return diag.Capability{
		ID:          "jvm.jmx.local",
		Category:    "management",
		Status:      statusSince(major, 8),
		Since:       "8",
		Command:     []string{"jcmd", "<pid>", "ManagementAgent.status"},
		Description: "Local JMX management-agent discovery and startup.",
	}
}

func statusSince(major, since int) diag.CapabilityStatus {
	if major == 0 {
		return diag.CapabilityUnknown
	}
	if major < since {
		return diag.CapabilityUnavailable
	}
	return diag.CapabilityAvailable
}

func parseJavaMajor(version string) int {
	version = strings.TrimSpace(version)
	if version == "" {
		return 0
	}
	if strings.HasPrefix(version, "1.") {
		parts := strings.Split(version, ".")
		if len(parts) >= 2 {
			n, _ := strconv.Atoi(parts[1])
			return n
		}
	}
	var b strings.Builder
	for _, r := range version {
		if r < '0' || r > '9' {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return 0
	}
	n, err := strconv.Atoi(b.String())
	if err != nil {
		return 0
	}
	return n
}

func virtualThreadEra(version string) string {
	major := parseJavaMajor(version)
	switch {
	case major == 0:
		return "unknown"
	case major < 21:
		return "pre-virtual-thread-production"
	case major < 24:
		return "virtual-thread-production-baseline"
	case major < 25:
		return "jdk24-virtual-thread-pinning-improvements"
	default:
		return fmt.Sprintf("jdk%d-virtual-thread-era", major)
	}
}
