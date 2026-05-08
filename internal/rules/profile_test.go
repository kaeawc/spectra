package rules

import (
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestMatchProfile_JetBrainsToolbox(t *testing.T) {
	j := jvm.Info{
		PID:      1127,
		JavaHome: "/Applications/JetBrains Toolbox.app/Contents/jre/Contents/Home",
	}
	got := MatchProfile(j, BuiltinProfiles())
	if got == nil || got.ID != "jetbrains-toolbox" {
		t.Fatalf("expected jetbrains-toolbox, got %v", got)
	}
	if !HasTag(got, TagTightHeapExpected) {
		t.Errorf("toolbox should carry tight_heap_expected tag")
	}
	if !HasTag(got, TagLauncher) {
		t.Errorf("toolbox should carry launcher tag")
	}
}

func TestMatchProfile_GradleDaemon(t *testing.T) {
	j := jvm.Info{
		PID:       9001,
		MainClass: "org.gradle.launcher.daemon.bootstrap.GradleDaemon",
	}
	got := MatchProfile(j, BuiltinProfiles())
	if got == nil || got.ID != "gradle-daemon" {
		t.Fatalf("expected gradle-daemon, got %v", got)
	}
	if !HasTag(got, TagBuildToolDaemon) {
		t.Errorf("gradle daemon should carry build_tool_daemon tag")
	}
}

func TestMatchProfile_NoMatch(t *testing.T) {
	j := jvm.Info{PID: 1, MainClass: "com.example.MyServer", JavaHome: "/opt/java"}
	if got := MatchProfile(j, BuiltinProfiles()); got != nil {
		t.Errorf("plain server should not match any builtin profile, got %v", got)
	}
}

func TestMatchProfile_AllMatchersMustHold(t *testing.T) {
	// Android Studio requires BOTH java home and main class.
	jOnlyHome := jvm.Info{JavaHome: "/Applications/Android Studio.app/Contents/jbr/Contents/Home"}
	if got := MatchProfile(jOnlyHome, BuiltinProfiles()); got != nil && got.ID == "android-studio" {
		t.Errorf("android-studio should require main class match too, got %v", got)
	}
}

func TestMatchProfile_EmptyProfileNeverMatches(t *testing.T) {
	empty := []AppProfile{{ID: "empty"}}
	j := jvm.Info{MainClass: "anything", JavaHome: "/anywhere", VMArgs: "-Xmx2g"}
	if got := MatchProfile(j, empty); got != nil {
		t.Errorf("a profile with zero matchers must never match, got %v", got)
	}
}

func TestHasTag_Nil(t *testing.T) {
	if HasTag(nil, TagTightHeapExpected) {
		t.Error("HasTag(nil, ...) must be false")
	}
}

func TestMatchProfile_VMArgsHas(t *testing.T) {
	catalog := []AppProfile{{
		ID:        "kotlin-compose",
		VMArgsHas: []string{"-Dskiko.metal", "-Xmx190m"},
		Tags:      []string{"compose-desktop"},
	}}
	matching := jvm.Info{VMArgs: "-Xmx190m -Dskiko.metal.gpu.priority=integrated"}
	if got := MatchProfile(matching, catalog); got == nil {
		t.Error("expected match when both vmargs needles present")
	}
	missingOne := jvm.Info{VMArgs: "-Xmx190m"}
	if got := MatchProfile(missingOne, catalog); got != nil {
		t.Error("expected no match when one vmargs needle absent")
	}
}
