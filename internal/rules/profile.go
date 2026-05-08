package rules

import (
	"strings"

	"github.com/kaeawc/spectra/internal/jvm"
)

// AppProfile classifies a running JVM as a known application kind so rules
// can recalibrate (or suppress) generic findings based on what the app is,
// not just what its metrics look like at one moment.
//
// A profile is a tuple of optional matchers — empty matchers are skipped,
// non-empty matchers must all pass. Matching is conservative: prefer a
// false negative over a false positive, since a misattributed profile
// would silence legitimate findings.
type AppProfile struct {
	ID   string
	Name string

	// MainClassContains: if non-empty, jvm.Info.MainClass must contain this
	// substring (case-insensitive).
	MainClassContains string

	// JavaHomeContains: if non-empty, jvm.Info.JavaHome must contain this
	// substring (case-insensitive).
	JavaHomeContains string

	// VMArgsHas: every entry must appear as a substring in jvm.Info.VMArgs.
	VMArgsHas []string

	Tags []string
}

// Common tag names. Centralized so rule code and tests don't drift.
const (
	TagTightHeapExpected = "tight_heap_expected"
	TagLargeHeapExpected = "large_heap_expected"
	TagLauncher          = "launcher"
	TagBuildToolDaemon   = "build_tool_daemon"
	TagIDE               = "ide"
)

// BuiltinProfiles returns the in-tree catalog. Keep this list short and
// well-justified; vague matchers cause silent suppression bugs.
func BuiltinProfiles() []AppProfile {
	return []AppProfile{
		{
			ID:               "jetbrains-toolbox",
			Name:             "JetBrains Toolbox",
			JavaHomeContains: "jetbrains toolbox.app",
			Tags:             []string{TagLauncher, TagTightHeapExpected},
		},
		{
			ID:                "intellij-idea",
			Name:              "IntelliJ IDEA",
			MainClassContains: "com.intellij.idea.main",
			Tags:              []string{TagIDE, TagLargeHeapExpected},
		},
		{
			ID:                "android-studio",
			Name:              "Android Studio",
			JavaHomeContains:  "android studio.app",
			MainClassContains: "com.intellij.idea.main",
			Tags:              []string{TagIDE, TagLargeHeapExpected},
		},
		{
			ID:                "gradle-daemon",
			Name:              "Gradle Daemon",
			MainClassContains: "org.gradle.launcher.daemon",
			Tags:              []string{TagBuildToolDaemon, TagTightHeapExpected},
		},
		{
			ID:                "bazel-server",
			Name:              "Bazel Server",
			MainClassContains: "com.google.devtools.build.lib.bazel.bazelserver",
			Tags:              []string{TagBuildToolDaemon},
		},
	}
}

// MatchProfile returns the first profile in catalog that matches j, or nil.
// Use BuiltinProfiles() as catalog unless you have user overrides loaded.
func MatchProfile(j jvm.Info, catalog []AppProfile) *AppProfile {
	for i := range catalog {
		p := &catalog[i]
		if !matchesProfile(j, p) {
			continue
		}
		return p
	}
	return nil
}

func matchesProfile(j jvm.Info, p *AppProfile) bool {
	if p.MainClassContains == "" && p.JavaHomeContains == "" && len(p.VMArgsHas) == 0 {
		return false // a profile with zero matchers should never match
	}
	if p.MainClassContains != "" {
		if !containsFold(j.MainClass, p.MainClassContains) {
			return false
		}
	}
	if p.JavaHomeContains != "" {
		if !containsFold(j.JavaHome, p.JavaHomeContains) {
			return false
		}
	}
	for _, needle := range p.VMArgsHas {
		if !strings.Contains(j.VMArgs, needle) {
			return false
		}
	}
	return true
}

// HasTag reports whether the profile carries the named tag. Nil profiles
// have no tags — callers can pass MatchProfile's result directly.
func HasTag(p *AppProfile, tag string) bool {
	if p == nil {
		return false
	}
	for _, t := range p.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
