package rules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/toolchain"
)

func baseSnap() snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:   "snap-test",
		Kind: snapshot.KindLive,
		Host: snapshot.HostInfo{
			Hostname: "test-mac",
			RAMBytes: 16 * 1024 * 1024 * 1024, // 16 GiB
		},
	}
}

// --- parseMajor ---

func TestParseMajor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"21.0.6", 21},
		{"17.0.10", 17},
		{"11.0.22", 11},
		{"1.8.0_402", 8},
		{"1.11.0", 11}, // unusual legacy prefix
		{"9.0.4", 9},
		{"bad", 0},
	}
	for _, c := range cases {
		if got := parseMajor(c.in); got != c.want {
			t.Errorf("parseMajor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// --- parseXmxMB ---

func TestParseXmxMB(t *testing.T) {
	cases := []struct {
		args string
		want int64
	}{
		{"-Xmx4g -Xms256m", 4096},
		{"-Xmx2048m", 2048},
		{"-Xmx512m", 512},
		{"-Xmx1G", 1024},
		{"-Xms256m", 0}, // no -Xmx
		{"", 0},
	}
	for _, c := range cases {
		if got := parseXmxMB(c.args); got != c.want {
			t.Errorf("parseXmxMB(%q) = %d, want %d", c.args, got, c.want)
		}
	}
}

// --- jvm-eol-version ---

func TestJVMEOLVersionFires(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{
		{PID: 100, MainClass: "com.example.App", JDKVersion: "11.0.22"}, // supported
		{PID: 200, MainClass: "com.old.App", JDKVersion: "1.8.0_402"},   // 8 = supported
		{PID: 300, MainClass: "com.ancient.App", JDKVersion: "9.0.4"},   // 9 = EOL non-LTS
		{PID: 400, MainClass: "com.jdk7.App", JDKVersion: "7.0.80"},     // < 8 = EOL
	}
	findings := ruleJVMEOLVersion().MatchFn(s)
	if len(findings) != 2 {
		t.Fatalf("expected 2 EOL findings (JDK 9 and JDK 7), got %d: %v", len(findings), findings)
	}
}

func TestJVMEOLVersionNoFire(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{
		{PID: 1, JDKVersion: "21.0.6"},
		{PID: 2, JDKVersion: "17.0.10"},
		{PID: 3, JDKVersion: "11.0.22"},
	}
	if findings := ruleJVMEOLVersion().MatchFn(s); len(findings) != 0 {
		t.Errorf("expected no findings for supported JDK versions, got %v", findings)
	}
}

// --- jvm-heap-vs-system ---

func TestJVMHeapVsSystemFires(t *testing.T) {
	s := baseSnap() // 16 GiB RAM → 16384 MiB
	s.JVMs = []jvm.Info{
		{PID: 1, MainClass: "big.App", VMArgs: "-Xmx12g"}, // 12288 MiB = 75% → fires
		{PID: 2, MainClass: "ok.App", VMArgs: "-Xmx4g"},   // 4096 MiB = 25% → no fire
	}
	findings := ruleJVMHeapVsSystemRAM().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 heap finding, got %d: %v", len(findings), findings)
	}
	if findings[0].Severity != SeverityHigh {
		t.Errorf("severity = %q, want high", findings[0].Severity)
	}
}

func TestJVMHeapVsSystemNoFire(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{PID: 1, VMArgs: "-Xmx2g"}} // 12.5% → ok
	if findings := ruleJVMHeapVsSystemRAM().MatchFn(s); len(findings) != 0 {
		t.Errorf("expected no findings for -Xmx2g on 16 GiB, got %v", findings)
	}
}

// --- jdk-major-version-drift ---

func TestJDKDriftFires(t *testing.T) {
	s := baseSnap()
	s.Toolchains.JDKs = []toolchain.JDKInstall{
		{VersionMajor: 21, Vendor: "Eclipse Adoptium", Source: "brew"},
		{VersionMajor: 21, Vendor: "Azul Zulu", Source: "sdkman"},
		{VersionMajor: 17, Vendor: "Oracle", Source: "manual"},
	}
	findings := ruleJDKMajorVersionDrift().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 drift finding (JDK 21 x2), got %d: %v", len(findings), findings)
	}
	if findings[0].Subject != "JDK 21 (2 installs)" {
		t.Errorf("subject = %q", findings[0].Subject)
	}
}

func TestJDKDriftNoFire(t *testing.T) {
	s := baseSnap()
	s.Toolchains.JDKs = []toolchain.JDKInstall{
		{VersionMajor: 21, Vendor: "Eclipse Adoptium"},
		{VersionMajor: 17, Vendor: "Eclipse Adoptium"},
	}
	if findings := ruleJDKMajorVersionDrift().MatchFn(s); len(findings) != 0 {
		t.Errorf("expected no drift for different major versions, got %v", findings)
	}
}

// --- java-home-mismatch ---

func TestJavaHomeMismatchFires(t *testing.T) {
	s := baseSnap()
	s.Toolchains.Env.JavaHome = "/nonexistent/path/jdk"
	s.Toolchains.JDKs = []toolchain.JDKInstall{
		{Path: "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home"},
	}
	findings := ruleJavaHomeMismatch().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 mismatch finding, got %d", len(findings))
	}
}

func TestJavaHomeMismatchNoFire(t *testing.T) {
	s := baseSnap()
	s.Toolchains.Env.JavaHome = "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home"
	s.Toolchains.JDKs = []toolchain.JDKInstall{
		{Path: "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home"},
	}
	if findings := ruleJavaHomeMismatch().MatchFn(s); len(findings) != 0 {
		t.Errorf("expected no mismatch when JAVA_HOME matches a JDK, got %v", findings)
	}
}

func TestJavaHomeMissingEnv(t *testing.T) {
	s := baseSnap() // JAVA_HOME empty
	if findings := ruleJavaHomeMismatch().MatchFn(s); len(findings) != 0 {
		t.Errorf("expected no finding when JAVA_HOME not set, got %v", findings)
	}
}

// --- library storage footprint ---

func TestStorageFootprintFires(t *testing.T) {
	s := baseSnap()
	s.Storage = storagestate.State{UserLibraryBytes: 6 * 1024 * 1024 * 1024} // 6 GiB
	findings := ruleStorageFootprint().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 storage finding, got %d", len(findings))
	}
	if findings[0].Severity != SeverityInfo {
		t.Errorf("severity = %q, want info", findings[0].Severity)
	}
}

func TestStorageFootprintNoFire(t *testing.T) {
	s := baseSnap()
	s.Storage = storagestate.State{UserLibraryBytes: 2 * 1024 * 1024 * 1024} // 2 GiB
	if findings := ruleStorageFootprint().MatchFn(s); len(findings) != 0 {
		t.Errorf("expected no finding for 2 GiB ~/Library, got %v", findings)
	}
}

// --- app-no-hardened-runtime ---

func TestAppNoHardenedRuntimeFires(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/Signed.app", TeamID: "TEAMID123", HardenedRuntime: false, MASReceipt: false},
	}
	findings := ruleAppNoHardenedRuntime().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "app-no-hardened-runtime" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
}

func TestAppNoHardenedRuntimeMASExcluded(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/MASApp.app", TeamID: "TEAMID123", HardenedRuntime: false, MASReceipt: true},
	}
	if findings := ruleAppNoHardenedRuntime().MatchFn(s); len(findings) != 0 {
		t.Errorf("MAS apps should be excluded, got %v", findings)
	}
}

func TestAppNoHardenedRuntimeUnsignedExcluded(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/Unsigned.app", TeamID: "", HardenedRuntime: false},
	}
	if findings := ruleAppNoHardenedRuntime().MatchFn(s); len(findings) != 0 {
		t.Errorf("unsigned apps should be excluded (caught by app-unsigned), got %v", findings)
	}
}

func TestAppNoHardenedRuntimeAlreadyHardened(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/Good.app", TeamID: "TEAMID123", HardenedRuntime: true},
	}
	if findings := ruleAppNoHardenedRuntime().MatchFn(s); len(findings) != 0 {
		t.Errorf("hardened app should produce no findings, got %v", findings)
	}
}

// --- app-unsigned ---

func TestAppUnsignedFires(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/NoSig.app", TeamID: ""},
	}
	findings := ruleAppUnsigned().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
}

func TestAppUnsignedNoFire(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/Signed.app", TeamID: "TEAMID123"},
	}
	if findings := ruleAppUnsigned().MatchFn(s); len(findings) != 0 {
		t.Errorf("signed app should produce no findings, got %v", findings)
	}
}

// --- login-item-dangling ---

func TestLoginItemDanglingFires(t *testing.T) {
	dir := t.TempDir()
	// Create a plist file in the temp dir (exists on disk) but the app bundle does not.
	plistPath := filepath.Join(dir, "com.dead.app.plist")
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(dir, "DeadApp.app") // does NOT exist

	s := baseSnap()
	s.Apps = []detect.Result{
		{
			Path: appPath,
			LoginItems: []detect.LoginItem{
				{Path: plistPath, Label: "com.dead.app", Scope: "system"},
			},
		},
	}
	findings := ruleLoginItemDangling().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 dangling finding, got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "login-item-dangling" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
}

func TestLoginItemDanglingAppExists(t *testing.T) {
	dir := t.TempDir()
	plistPath := filepath.Join(dir, "com.alive.app.plist")
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// App bundle exists.
	appPath := filepath.Join(dir, "AliveApp.app")
	if err := os.Mkdir(appPath, 0o755); err != nil {
		t.Fatal(err)
	}

	s := baseSnap()
	s.Apps = []detect.Result{
		{
			Path: appPath,
			LoginItems: []detect.LoginItem{
				{Path: plistPath, Label: "com.alive.app", Scope: "user"},
			},
		},
	}
	if findings := ruleLoginItemDangling().MatchFn(s); len(findings) != 0 {
		t.Errorf("live app should produce no dangling finding, got %v", findings)
	}
}

// --- permission-mismatch ---

func TestPermissionMismatchFires(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{
			Path:               "/Applications/Sneaky.app",
			TeamID:             "TEAM1",
			GrantedPermissions: []string{"Camera", "Microphone"},
			PrivacyDescriptions: map[string]string{
				// NSCameraUsageDescription declared, NSMicrophoneUsageDescription missing
				"NSCameraUsageDescription": "Used for video calls",
			},
		},
	}
	findings := rulePermissionMismatch().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (Microphone missing NS key), got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "permission-mismatch" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
}

func TestPermissionMismatchBothDeclared(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{
			Path:               "/Applications/GoodApp.app",
			TeamID:             "TEAM1",
			GrantedPermissions: []string{"Camera", "Microphone"},
			PrivacyDescriptions: map[string]string{
				"NSCameraUsageDescription":     "Video calls",
				"NSMicrophoneUsageDescription": "Audio calls",
			},
		},
	}
	if findings := rulePermissionMismatch().MatchFn(s); len(findings) != 0 {
		t.Errorf("all NS keys declared — expected no findings, got %v", findings)
	}
}

func TestPermissionMismatchUnknownServiceIgnored(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{
			Path:                "/Applications/FDA.app",
			TeamID:              "TEAM1",
			GrantedPermissions:  []string{"SystemPolicyAllFiles", "Accessibility"},
			PrivacyDescriptions: map[string]string{},
		},
	}
	// SystemPolicyAllFiles and Accessibility have no required NS key — rule must be silent.
	if findings := rulePermissionMismatch().MatchFn(s); len(findings) != 0 {
		t.Errorf("services with no NS requirement should be ignored, got %v", findings)
	}
}

func TestPermissionMismatchNoGrantedPermissions(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/Plain.app", TeamID: "TEAM1"},
	}
	if findings := rulePermissionMismatch().MatchFn(s); len(findings) != 0 {
		t.Errorf("app with no granted permissions should produce no findings, got %v", findings)
	}
}

// --- sparse-file-inflation ---

func TestSparseFileInflationFires(t *testing.T) {
	s := baseSnap()
	actual := int64(5 * 1024 * 1024 * 1024)    // 5 GiB real
	apparent := int64(60 * 1024 * 1024 * 1024) // 60 GiB logical (12×)
	s.Apps = []detect.Result{
		{
			Path:              "/Applications/Docker.app",
			BundleSizeBytes:   actual,
			ApparentSizeBytes: apparent,
		},
	}
	findings := ruleSparseFileInflation().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 inflation finding, got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "sparse-file-inflation" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
}

func TestSparseFileInflationBelowThreshold(t *testing.T) {
	s := baseSnap()
	actual := int64(10 * 1024 * 1024 * 1024)   // 10 GiB
	apparent := int64(15 * 1024 * 1024 * 1024) // 15 GiB (1.5×)
	s.Apps = []detect.Result{
		{
			Path:              "/Applications/BigApp.app",
			BundleSizeBytes:   actual,
			ApparentSizeBytes: apparent,
		},
	}
	if findings := ruleSparseFileInflation().MatchFn(s); len(findings) != 0 {
		t.Errorf("1.5× inflation is below threshold — expected no findings, got %v", findings)
	}
}

func TestSparseFileInflationSmallBundle(t *testing.T) {
	s := baseSnap()
	// Bundle < 1 MiB actual — rule should stay silent to avoid noise.
	s.Apps = []detect.Result{
		{
			Path:              "/Applications/Tiny.app",
			BundleSizeBytes:   100 * 1024,               // 100 KiB actual
			ApparentSizeBytes: 100 * 1024 * 1024 * 1024, // 100 GiB apparent (absurd edge case)
		},
	}
	if findings := ruleSparseFileInflation().MatchFn(s); len(findings) != 0 {
		t.Errorf("small actual size should suppress firing, got %v", findings)
	}
}

// --- Evaluate (engine) ---

func TestEvaluateSortOrder(t *testing.T) {
	s := baseSnap()
	// RAMBytes = 16 GiB
	s.JVMs = []jvm.Info{
		{PID: 1, MainClass: "big", VMArgs: "-Xmx12g"},   // high
		{PID: 2, MainClass: "old", JDKVersion: "9.0.4"}, // medium
	}
	s.Storage = storagestate.State{UserLibraryBytes: 10 * 1024 * 1024 * 1024} // info
	findings := Evaluate(s, V1Catalog())
	if len(findings) < 3 {
		t.Fatalf("expected at least 3 findings, got %d", len(findings))
	}
	// First finding must be high severity.
	if findings[0].Severity != SeverityHigh {
		t.Errorf("first finding severity = %q, want high", findings[0].Severity)
	}
}

// --- ruleGatekeeperRejected ---

func TestGatekeeperRejectedFires(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/BadApp.app", GatekeeperStatus: "rejected", TeamID: "ABC123"},
	}
	findings := ruleGatekeeperRejected().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].RuleID != "app-gatekeeper-rejected" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
	if findings[0].Severity != SeverityHigh {
		t.Errorf("severity = %q, want high", findings[0].Severity)
	}
}

func TestGatekeeperRejectedNoFireAccepted(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/GoodApp.app", GatekeeperStatus: "accepted"},
	}
	findings := ruleGatekeeperRejected().MatchFn(s)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for accepted app, got %d", len(findings))
	}
}

func TestGatekeeperRejectedNoFireUnknown(t *testing.T) {
	s := baseSnap()
	s.Apps = []detect.Result{
		{Path: "/Applications/UnknownApp.app", GatekeeperStatus: ""},
	}
	findings := ruleGatekeeperRejected().MatchFn(s)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for unknown status, got %d", len(findings))
	}
}

func TestBrewDeprecatedFormulaFires(t *testing.T) {
	s := baseSnap()
	s.Toolchains.Brew.Formulae = []toolchain.BrewFormula{
		{Name: "wget", Version: "1.21.4", Deprecated: false},
		{Name: "ossp-uuid", Version: "1.6.2", Deprecated: true},
	}
	findings := ruleBrewDeprecatedFormula().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].RuleID != "brew-deprecated-formula" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
	if findings[0].Subject != "ossp-uuid" {
		t.Errorf("subject = %q, want ossp-uuid", findings[0].Subject)
	}
}

func TestBrewDeprecatedFormulaNoFire(t *testing.T) {
	s := baseSnap()
	s.Toolchains.Brew.Formulae = []toolchain.BrewFormula{
		{Name: "git", Version: "2.44.0", Deprecated: false},
	}
	findings := ruleBrewDeprecatedFormula().MatchFn(s)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

func TestBrewStalePinnedFires(t *testing.T) {
	s := baseSnap()
	s.Toolchains.Brew.Formulae = []toolchain.BrewFormula{
		{Name: "node@18", Version: "18.20.4", Pinned: true},
		{Name: "python@3.12", Version: "3.12.3", Pinned: false},
	}
	findings := ruleBrewStalePinned().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].RuleID != "brew-stale-pinned" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
	if findings[0].Subject != "node@18" {
		t.Errorf("subject = %q, want node@18", findings[0].Subject)
	}
}

func TestBrewStalePinnedNoFire(t *testing.T) {
	s := baseSnap()
	s.Toolchains.Brew.Formulae = []toolchain.BrewFormula{
		{Name: "wget", Version: "1.21.4", Pinned: false},
	}
	findings := ruleBrewStalePinned().MatchFn(s)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

func TestBrewStalePinnedMultiple(t *testing.T) {
	s := baseSnap()
	s.Toolchains.Brew.Formulae = []toolchain.BrewFormula{
		{Name: "node@18", Version: "18.20.4", Pinned: true},
		{Name: "maven", Version: "3.9.6", Pinned: true},
	}
	findings := ruleBrewStalePinned().MatchFn(s)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
}

func TestV1CatalogContainsBrewRules(t *testing.T) {
	catalog := V1Catalog()
	ruleIDs := make(map[string]bool)
	for _, r := range catalog {
		ruleIDs[r.ID] = true
	}
	for _, want := range []string{"brew-deprecated-formula", "brew-stale-pinned"} {
		if !ruleIDs[want] {
			t.Errorf("V1Catalog missing rule %q", want)
		}
	}
}

func TestPathShadowsActiveRuntimeFires(t *testing.T) {
	s := baseSnap()
	// nvm is installed but system node is active
	s.Toolchains.Node = []toolchain.RuntimeInstall{
		{Version: "v18.20.4", Source: "nvm", Path: "/home/user/.nvm/versions/node/v18.20.4/bin/node", Active: false},
		{Version: "system", Source: "system", Path: "/usr/bin/node", Active: true},
	}
	findings := rulePathShadowsActiveRuntime().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].RuleID != "path-shadows-active-runtime" {
		t.Errorf("rule ID = %q", findings[0].RuleID)
	}
	if findings[0].Subject != "node" {
		t.Errorf("subject = %q, want node", findings[0].Subject)
	}
}

func TestPathShadowsActiveRuntimeNoFireManagerActive(t *testing.T) {
	s := baseSnap()
	// nvm is installed AND nvm-managed version is active — no finding
	s.Toolchains.Node = []toolchain.RuntimeInstall{
		{Version: "v18.20.4", Source: "nvm", Path: "/home/user/.nvm/versions/node/v18.20.4/bin/node", Active: true},
		{Version: "system", Source: "system", Path: "/usr/bin/node", Active: false},
	}
	findings := rulePathShadowsActiveRuntime().MatchFn(s)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

func TestPathShadowsActiveRuntimeNoFireOnlySystem(t *testing.T) {
	s := baseSnap()
	// only system install, no manager — not a problem
	s.Toolchains.Node = []toolchain.RuntimeInstall{
		{Version: "system", Source: "system", Path: "/usr/bin/node", Active: true},
	}
	findings := rulePathShadowsActiveRuntime().MatchFn(s)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

func TestPathShadowsActiveRuntimeMultipleLanguages(t *testing.T) {
	s := baseSnap()
	// pyenv installed but brew python active; goenv installed but brew go active
	s.Toolchains.Python = []toolchain.RuntimeInstall{
		{Version: "3.12.3", Source: "pyenv", Active: false},
		{Version: "system", Source: "brew", Active: true},
	}
	s.Toolchains.Go = []toolchain.RuntimeInstall{
		{Version: "1.22.3", Source: "goenv", Active: false},
		{Version: "system", Source: "brew", Active: true},
	}
	findings := rulePathShadowsActiveRuntime().MatchFn(s)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	langs := map[string]bool{}
	for _, f := range findings {
		langs[f.Subject] = true
	}
	if !langs["python"] || !langs["go"] {
		t.Errorf("expected python and go in findings: %v", langs)
	}
}
