package rules

import (
	"testing"

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

// --- Evaluate (engine) ---

func TestEvaluateSortOrder(t *testing.T) {
	s := baseSnap()
	// RAMBytes = 16 GiB
	s.JVMs = []jvm.Info{
		{PID: 1, MainClass: "big", VMArgs: "-Xmx12g"}, // high
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
