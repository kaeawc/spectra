package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// V1Catalog returns all built-in rules for the V1 release.
// Rules are ordered by severity (high first) within each category.
func V1Catalog() []Rule {
	return []Rule{
		ruleJVMEOLVersion(),
		ruleJVMHeapVsSystemRAM(),
		ruleJDKMajorVersionDrift(),
		ruleJavaHomeMismatch(),
		ruleStorageFootprint(),
	}
}

// ruleJVMEOLVersion fires for each running JVM using a JDK major version
// that is past Oracle's public support window.
func ruleJVMEOLVersion() Rule {
	// Oracle's public support EOL dates (as of 2026).
	// Versions 8 (LTS), 11 (LTS), 17 (LTS), 21 (LTS) still supported.
	// Versions 9, 10, 12-16, 18-20, 22+ (non-LTS) are EOL.
	eolNonLTS := map[int]bool{9: true, 10: true, 12: true, 13: true, 14: true,
		15: true, 16: true, 18: true, 19: true, 20: true, 22: true, 23: true, 24: true}

	return Rule{
		ID:       "jvm-eol-version",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, j := range s.JVMs {
				if j.JDKVersion == "" {
					continue
				}
				major := parseMajor(j.JDKVersion)
				if major == 0 {
					continue
				}
				// Versions < 8 are clearly EOL; non-LTS post-8 are also EOL.
				if major < 8 || eolNonLTS[major] {
					findings = append(findings, Finding{
						RuleID:   "jvm-eol-version",
						Severity: SeverityMedium,
						Subject:  fmt.Sprintf("PID %d (%s)", j.PID, j.MainClass),
						Message:  fmt.Sprintf("JDK %s (major %d) is past public support.", j.JDKVersion, major),
						Fix:      "Upgrade to JDK 21 LTS or JDK 17 LTS. Run `spectra jvm` to see all running JVMs.",
					})
				}
			}
			return findings
		},
	}
}

// ruleJVMHeapVsSystemRAM fires when a running JVM's -Xmx setting allocates
// more than 60% of system RAM, which can cause OS-level swap pressure.
func ruleJVMHeapVsSystemRAM() Rule {
	return Rule{
		ID:       "jvm-heap-vs-system",
		Severity: SeverityHigh,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			ramMB := int64(s.Host.RAMBytes / (1024 * 1024))
			if ramMB == 0 {
				return nil
			}
			var findings []Finding
			for _, j := range s.JVMs {
				if j.VMArgs == "" {
					continue
				}
				xmxMB := parseXmxMB(j.VMArgs)
				if xmxMB <= 0 {
					continue
				}
				pct := xmxMB * 100 / ramMB
				if pct > 60 {
					findings = append(findings, Finding{
						RuleID:   "jvm-heap-vs-system",
						Severity: SeverityHigh,
						Subject:  fmt.Sprintf("PID %d (%s)", j.PID, j.MainClass),
						Message:  fmt.Sprintf("-Xmx%dMB is %d%% of system RAM (%dMB). Swap thrashing likely under GC pressure.", xmxMB, pct, ramMB),
						Fix:      "Reduce -Xmx, or if this is intentional ensure sufficient swap headroom.",
					})
				}
			}
			return findings
		},
	}
}

// ruleJDKMajorVersionDrift fires when more than one installed JDK shares
// the same major version but different patch/vendor combinations, which
// can cause confusing version resolution.
func ruleJDKMajorVersionDrift() Rule {
	return Rule{
		ID:       "jdk-major-version-drift",
		Severity: SeverityLow,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			// Group JDKs by major version.
			byMajor := make(map[int][]toolchain.JDKInstall)
			for _, j := range s.Toolchains.JDKs {
				byMajor[j.VersionMajor] = append(byMajor[j.VersionMajor], j)
			}
			var findings []Finding
			// Stable iteration order.
			majors := make([]int, 0, len(byMajor))
			for m := range byMajor {
				majors = append(majors, m)
			}
			sort.Ints(majors)
			for _, m := range majors {
				group := byMajor[m]
				if len(group) <= 1 {
					continue
				}
				paths := make([]string, len(group))
				for i, j := range group {
					paths[i] = fmt.Sprintf("%s %s", j.Vendor, j.ReleaseString)
				}
				findings = append(findings, Finding{
					RuleID:   "jdk-major-version-drift",
					Severity: SeverityLow,
					Subject:  fmt.Sprintf("JDK %d (%d installs)", m, len(group)),
					Message:  fmt.Sprintf("%d JDK %d installs found: %s", len(group), m, strings.Join(paths, "; ")),
					Fix:      "Remove duplicate JDK installations to avoid version-resolution surprises.",
				})
			}
			return findings
		},
	}
}

// ruleJavaHomeMismatch fires when JAVA_HOME points to a path not found in
// the list of discovered JDK installs.
func ruleJavaHomeMismatch() Rule {
	return Rule{
		ID:       "java-home-mismatch",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			javaHome := s.Toolchains.Env.JavaHome
			if javaHome == "" {
				return nil
			}
			for _, j := range s.Toolchains.JDKs {
				if strings.HasPrefix(j.Path, javaHome) || strings.HasPrefix(javaHome, j.Path) {
					return nil // found a match
				}
			}
			return []Finding{{
				RuleID:   "java-home-mismatch",
				Severity: SeverityMedium,
				Subject:  javaHome,
				Message:  fmt.Sprintf("JAVA_HOME=%q does not match any discovered JDK installation.", javaHome),
				Fix:      "Run `spectra jvm` to see discovered JDKs and update JAVA_HOME in your shell profile.",
			}}
		},
	}
}

// ruleStorageFootprint fires when ~/Library exceeds 5 GB (potential bloat).
func ruleStorageFootprint() Rule {
	const threshold = 5 * 1024 * 1024 * 1024 // 5 GiB
	return Rule{
		ID:       "library-storage-footprint",
		Severity: SeverityInfo,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			if s.Storage.UserLibraryBytes < threshold {
				return nil
			}
			gb := float64(s.Storage.UserLibraryBytes) / (1024 * 1024 * 1024)
			return []Finding{{
				RuleID:   "library-storage-footprint",
				Severity: SeverityInfo,
				Message:  fmt.Sprintf("~/Library is %.1f GB — above the 5 GB alert threshold.", gb),
				Fix:      "Run `spectra snapshot` and check LargestApps to identify top consumers.",
			}}
		},
	}
}

// parseMajor extracts the major version number from a Java version string.
// Handles both legacy ("1.8.0_362") and modern ("21.0.6") formats.
func parseMajor(v string) int {
	// Legacy format: "1.8.0_N" → major 8
	if strings.HasPrefix(v, "1.") {
		parts := strings.SplitN(v, ".", 3)
		if len(parts) >= 2 {
			return atoi(parts[1])
		}
	}
	// Modern: "21.0.6" → 21
	parts := strings.SplitN(v, ".", 2)
	return atoi(parts[0])
}

// parseXmxMB extracts the -Xmx heap ceiling in MiB from a VM args string.
// Returns 0 if not found.
func parseXmxMB(vmArgs string) int64 {
	for _, part := range strings.Fields(vmArgs) {
		if !strings.HasPrefix(part, "-Xmx") {
			continue
		}
		raw := part[4:] // strip -Xmx
		if len(raw) == 0 {
			continue
		}
		suffix := raw[len(raw)-1]
		digits := raw
		if suffix >= 'A' && suffix <= 'z' {
			digits = raw[:len(raw)-1]
		}
		n := int64(atoi(digits))
		if n == 0 {
			continue
		}
		switch suffix {
		case 'g', 'G':
			return n * 1024
		case 'm', 'M':
			return n
		case 'k', 'K':
			return n / 1024
		default:
			return n / (1024 * 1024) // bare bytes → MiB
		}
	}
	return 0
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
