package rules

import (
	"fmt"
	"os"
	"path/filepath"
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
		ruleAppNoHardenedRuntime(),
		ruleAppUnsigned(),
		ruleLoginItemDangling(),
		ruleBrewDeprecatedFormula(),
		ruleBrewStalePinned(),
		rulePathShadowsActiveRuntime(),
		rulePermissionMismatch(),
		ruleSparseFileInflation(),
		ruleGatekeeperRejected(),
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
			const maxInt64 = uint64(1<<63 - 1)
			ramMB := s.Host.RAMBytes / (1024 * 1024)
			if ramMB == 0 || ramMB > maxInt64 {
				return nil
			}
			ramMBInt := int64(ramMB)
			var findings []Finding
			for _, j := range s.JVMs {
				if j.VMArgs == "" {
					continue
				}
				xmxMB := parseXmxMB(j.VMArgs)
				if xmxMB <= 0 {
					continue
				}
				pct := xmxMB * 100 / ramMBInt
				if pct > 60 {
					findings = append(findings, Finding{
						RuleID:   "jvm-heap-vs-system",
						Severity: SeverityHigh,
						Subject:  fmt.Sprintf("PID %d (%s)", j.PID, j.MainClass),
						Message:  fmt.Sprintf("-Xmx%dMB is %d%% of system RAM (%dMB). Swap thrashing likely under GC pressure.", xmxMB, pct, ramMBInt),
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

// ruleAppNoHardenedRuntime fires for Developer ID-signed apps that lack the
// hardened runtime entitlement. Hardened runtime is required for notarization
// and provides exploit-mitigation controls. MAS and unsigned apps are excluded.
func ruleAppNoHardenedRuntime() Rule {
	return Rule{
		ID:       "app-no-hardened-runtime",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, app := range s.Apps {
				// Skip unsigned (will be caught by app-unsigned) and MAS apps.
				if app.TeamID == "" || app.MASReceipt {
					continue
				}
				if !app.HardenedRuntime {
					findings = append(findings, Finding{
						RuleID:   "app-no-hardened-runtime",
						Severity: SeverityMedium,
						Subject:  appDisplayName(app.Path),
						Message:  fmt.Sprintf("%s is signed (team %s) but lacks hardened runtime — cannot be notarized and disables key exploit mitigations.", appDisplayName(app.Path), app.TeamID),
						Fix:      "Enable the Hardened Runtime capability in Xcode signing settings.",
					})
				}
			}
			return findings
		},
	}
}

// ruleAppUnsigned fires for apps with no Team ID (not code-signed by a
// Developer ID or Apple certificate). Unsigned apps bypass Gatekeeper.
func ruleAppUnsigned() Rule {
	return Rule{
		ID:       "app-unsigned",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, app := range s.Apps {
				if app.TeamID == "" {
					findings = append(findings, Finding{
						RuleID:   "app-unsigned",
						Severity: SeverityMedium,
						Subject:  appDisplayName(app.Path),
						Message:  fmt.Sprintf("%s has no code-signing Team ID — it is unsigned and bypasses Gatekeeper's signature checks.", appDisplayName(app.Path)),
						Fix:      "Only install apps from trusted, signed sources. If this app is expected to be unsigned, dismiss this finding.",
					})
				}
			}
			return findings
		},
	}
}

// ruleLoginItemDangling fires when a login-item plist exists on disk but the
// ProgramArguments executable it references no longer exists. This is the
// classic zombie-process pattern left behind after uninstalling an app.
func ruleLoginItemDangling() Rule {
	return Rule{
		ID:       "login-item-dangling",
		Severity: SeverityInfo,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, app := range s.Apps {
				for _, item := range app.LoginItems {
					if item.Path == "" {
						continue
					}
					// The plist must still exist; if it doesn't, launchd won't load it anyway.
					if _, err := os.Stat(item.Path); err != nil {
						continue
					}
					// We flag this item as dangling when the plist references a path that
					// can't be resolved back to an existing app bundle.
					// Heuristic: if the plist's app bundle path doesn't exist, it's orphaned.
					if !strings.HasPrefix(item.Path, app.Path) {
						// Plist isn't inside the app bundle — check if the app bundle still exists.
						if _, err := os.Stat(app.Path); err != nil {
							findings = append(findings, Finding{
								RuleID:   "login-item-dangling",
								Severity: SeverityInfo,
								Subject:  item.Label,
								Message:  fmt.Sprintf("Login item %q (%s) references app %q which no longer exists.", item.Label, item.Path, app.Path),
								Fix:      fmt.Sprintf("Remove the plist: sudo rm %q", item.Path),
							})
						}
					}
				}
			}
			return findings
		},
	}
}

// rulePermissionMismatch fires when an app has a TCC-granted permission
// that has no corresponding NS*UsageDescription in its Info.plist. Apps
// that silently receive TCC access without declaring why are a security
// concern and cannot pass App Review.
func rulePermissionMismatch() Rule {
	// Maps the human-readable TCC service name (kTCCService prefix stripped)
	// to the NS*UsageDescription key the app is expected to declare.
	// Services without a required NS key (e.g. SystemPolicyAllFiles, Accessibility)
	// are omitted — they don't require a usage description in Info.plist.
	tccToNSKey := map[string]string{
		"Camera":            "NSCameraUsageDescription",
		"Microphone":        "NSMicrophoneUsageDescription",
		"Contacts":          "NSContactsUsageDescription",
		"PhotoLibrary":      "NSPhotoLibraryUsageDescription",
		"PhotoLibraryAdd":   "NSPhotoLibraryAddUsageDescription",
		"Calendar":          "NSCalendarsUsageDescription",
		"Reminders":         "NSRemindersUsageDescription",
		"Bluetooth":         "NSBluetoothAlwaysUsageDescription",
		"SpeechRecognition": "NSSpeechRecognitionUsageDescription",
		"FaceID":            "NSFaceIDUsageDescription",
		"Motion":            "NSMotionUsageDescription",
		"HealthShare":       "NSHealthShareUsageDescription",
		"HealthUpdate":      "NSHealthUpdateUsageDescription",
		"HomeKit":           "NSHomeKitUsageDescription",
		"NearbyInteraction": "NSNearbyInteractionUsageDescription",
		"UserTracking":      "NSUserTrackingUsageDescription",
		"FocusStatus":       "NSFocusStatusUsageDescription",
		"LocalNetwork":      "NSLocalNetworkUsageDescription",
	}

	return Rule{
		ID:       "permission-mismatch",
		Severity: SeverityMedium,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, app := range s.Apps {
				for _, svc := range app.GrantedPermissions {
					nsKey, known := tccToNSKey[svc]
					if !known {
						continue
					}
					if _, declared := app.PrivacyDescriptions[nsKey]; !declared {
						findings = append(findings, Finding{
							RuleID:   "permission-mismatch",
							Severity: SeverityMedium,
							Subject:  appDisplayName(app.Path),
							Message:  fmt.Sprintf("%s has %s granted (TCC) but no %s in Info.plist.", appDisplayName(app.Path), svc, nsKey),
							Fix:      fmt.Sprintf("Add %s to %s/Contents/Info.plist, or revoke the TCC grant if this permission is unintended.", nsKey, app.Path),
						})
					}
				}
			}
			return findings
		},
	}
}

// ruleSparseFileInflation fires when an app bundle's apparent (logical) size
// exceeds its actual on-disk allocation by more than 10×. This is the
// signature of sparse disk images such as Docker Desktop's VM disk
// (~/Library/Containers/com.docker.docker/…/Docker.raw), which may
// appear as tens of GiB while occupying much less real space.
func ruleSparseFileInflation() Rule {
	const inflationFactor = 10

	return Rule{
		ID:       "sparse-file-inflation",
		Severity: SeverityInfo,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, app := range s.Apps {
				actual := app.BundleSizeBytes
				apparent := app.ApparentSizeBytes
				// Only fire when actual is non-trivial (>1 MiB) to avoid
				// divide-by-zero and noise from empty bundles.
				if actual < 1024*1024 || apparent <= actual*inflationFactor {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   "sparse-file-inflation",
					Severity: SeverityInfo,
					Subject:  appDisplayName(app.Path),
					Message:  fmt.Sprintf("%s apparent size %.1f GiB vs %.1f GiB actual (%.0f×) — likely a sparse disk image.", appDisplayName(app.Path), float64(apparent)/(1<<30), float64(actual)/(1<<30), float64(apparent)/float64(actual)),
					Fix:      "If this is a Docker or VM disk image, run `docker system prune` or compact the image to reclaim logical space.",
				})
			}
			return findings
		},
	}
}

// appDisplayName returns the human-readable app name from its .app bundle path.
func appDisplayName(appPath string) string {
	return strings.TrimSuffix(filepath.Base(appPath), ".app")
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

// ruleGatekeeperRejected fires when spctl --assess explicitly rejects an app.
// A "rejected" verdict means Gatekeeper would block the app from running on
// an unmodified macOS 10.15+ system.
func ruleGatekeeperRejected() Rule {
	return Rule{
		ID:       "app-gatekeeper-rejected",
		Severity: SeverityHigh,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, app := range s.Apps {
				if app.GatekeeperStatus != "rejected" {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   "app-gatekeeper-rejected",
					Severity: SeverityHigh,
					Subject:  appDisplayName(app.Path),
					Message:  fmt.Sprintf("%s is rejected by Gatekeeper — it would be blocked from running on an unmodified macOS system.", appDisplayName(app.Path)),
					Fix:      "Re-sign and notarize the app with a valid Apple Developer ID certificate, or remove it if it is no longer needed.",
				})
			}
			return findings
		},
	}
}

// rulePathShadowsActiveRuntime fires when a language-manager install (nvm,
// pyenv, rbenv, goenv, asdf, mise) is present but the active binary for that
// language resolves to a system or brew install instead — indicating that
// PATH is not configured to use the version manager.
func rulePathShadowsActiveRuntime() Rule {
	return Rule{
		ID:       "path-shadows-active-runtime",
		Severity: SeverityLow,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			tc := s.Toolchains
			groups := []struct {
				lang     string
				runtimes []toolchain.RuntimeInstall
			}{
				{"node", tc.Node},
				{"python", tc.Python},
				{"go", tc.Go},
				{"ruby", tc.Ruby},
			}
			managerSources := map[string]bool{
				"nvm": true, "fnm": true, "volta": true,
				"pyenv": true, "uv": true,
				"goenv": true,
				"rbenv": true,
				"asdf":  true, "mise": true,
			}
			var findings []Finding
			for _, g := range groups {
				if len(g.runtimes) == 0 {
					continue
				}
				hasManager := false
				activeSource := ""
				for _, r := range g.runtimes {
					if managerSources[r.Source] {
						hasManager = true
					}
					if r.Active {
						activeSource = r.Source
					}
				}
				if hasManager && (activeSource == "system" || activeSource == "brew") {
					findings = append(findings, Finding{
						RuleID:   "path-shadows-active-runtime",
						Severity: SeverityLow,
						Subject:  g.lang,
						Message:  fmt.Sprintf("A version manager is installed for %s but the active binary resolves to a %s install — PATH is not configured to use the version manager.", g.lang, activeSource),
						Fix:      fmt.Sprintf("Add the version manager's shim directory to the front of $PATH in your shell profile, or use `%s` to manage the active version.", activeSource),
					})
				}
			}
			return findings
		},
	}
}

// ruleBrewDeprecatedFormula fires for each Homebrew formula that is marked
// deprecated by its tap maintainer.
func ruleBrewDeprecatedFormula() Rule {
	return Rule{
		ID:       "brew-deprecated-formula",
		Severity: SeverityLow,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, f := range s.Toolchains.Brew.Formulae {
				if !f.Deprecated {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   "brew-deprecated-formula",
					Severity: SeverityLow,
					Subject:  f.Name,
					Message:  fmt.Sprintf("Homebrew formula %q is deprecated — the tap maintainer has marked it for removal.", f.Name),
					Fix:      fmt.Sprintf("Run `brew info %s` to see the recommended replacement, then uninstall this formula.", f.Name),
				})
			}
			return findings
		},
	}
}

// ruleBrewStalePinned fires for each Homebrew formula that is pinned,
// which prevents security and bug-fix updates from being applied.
func ruleBrewStalePinned() Rule {
	return Rule{
		ID:       "brew-stale-pinned",
		Severity: SeverityLow,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, f := range s.Toolchains.Brew.Formulae {
				if !f.Pinned {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   "brew-stale-pinned",
					Severity: SeverityLow,
					Subject:  f.Name,
					Message:  fmt.Sprintf("Homebrew formula %q is pinned at version %s — security and bug-fix updates will not be applied.", f.Name, f.Version),
					Fix:      fmt.Sprintf("Run `brew unpin %s` to allow updates, or verify the pin is still intentional.", f.Name),
				})
			}
			return findings
		},
	}
}
