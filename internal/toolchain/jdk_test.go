package toolchain

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// makeJDK creates a fake JDK home directory with a release file under dir.
func makeJDK(t *testing.T, dir, name, javaVersion, runtimeVersion, implementor string) string {
	t.Helper()
	jdkHome := filepath.Join(dir, name)
	if err := os.MkdirAll(jdkHome, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`JAVA_VERSION="%s"
JAVA_RUNTIME_VERSION="%s"
IMPLEMENTOR="%s"
`, javaVersion, runtimeVersion, implementor)
	if err := os.WriteFile(filepath.Join(jdkHome, "release"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return jdkHome
}

func TestParseJDKHome(t *testing.T) {
	dir := t.TempDir()
	makeJDK(t, dir, "jdk21", "21.0.6", "21.0.6+7-LTS", "Eclipse Adoptium")

	jdk, ok := parseJDKHome(filepath.Join(dir, "jdk21"), "sdkman")
	if !ok {
		t.Fatal("parseJDKHome returned false")
	}
	if jdk.VersionMajor != 21 {
		t.Errorf("VersionMajor = %d, want 21", jdk.VersionMajor)
	}
	if jdk.VersionMinor != 0 {
		t.Errorf("VersionMinor = %d, want 0", jdk.VersionMinor)
	}
	if jdk.VersionPatch != 6 {
		t.Errorf("VersionPatch = %d, want 6", jdk.VersionPatch)
	}
	if jdk.Vendor != "Eclipse Adoptium" {
		t.Errorf("Vendor = %q", jdk.Vendor)
	}
	if jdk.ReleaseString != "21.0.6+7-LTS" {
		t.Errorf("ReleaseString = %q", jdk.ReleaseString)
	}
	if jdk.Source != "sdkman" {
		t.Errorf("Source = %q", jdk.Source)
	}
}

func TestParseJDKHomeLegacy(t *testing.T) {
	// Old Java 8 style: JAVA_VERSION="1.8.0_392"
	dir := t.TempDir()
	makeJDK(t, dir, "jdk8", "1.8.0_392", "1.8.0_392-b08", "Azul Systems, Inc.")

	jdk, ok := parseJDKHome(filepath.Join(dir, "jdk8"), "brew")
	if !ok {
		t.Fatal("parseJDKHome returned false")
	}
	if jdk.VersionMajor != 8 {
		t.Errorf("VersionMajor = %d, want 8 (legacy 1.x prefix)", jdk.VersionMajor)
	}
}

func TestParseJDKHomeMissingRelease(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "empty"), 0o755)
	_, ok := parseJDKHome(filepath.Join(dir, "empty"), "manual")
	if ok {
		t.Error("expected false for JDK home with no release file")
	}
}

func TestDiscoverJDKsFromFakeSDKMAN(t *testing.T) {
	home := t.TempDir()
	sdkBase := filepath.Join(home, ".sdkman", "candidates", "java")
	makeJDK(t, sdkBase, "21.0.6-tem", "21.0.6", "21.0.6+7-LTS", "Eclipse Adoptium")
	makeJDK(t, sdkBase, "17.0.10-zulu", "17.0.10", "17.0.10+7", "Azul Systems")

	opts := CollectOptions{
		Home:          home,
		SystemJVMRoot: filepath.Join(home, "nonexistent-sys"),
		UserJVMRoot:   filepath.Join(home, "nonexistent-usr"),
		BrewCellars:   []string{filepath.Join(home, "cellar")},
	}
	jdks, err := discoverJDKs(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(jdks) != 2 {
		t.Fatalf("got %d JDKs, want 2; %+v", len(jdks), jdks)
	}
}

func TestDiscoverJDKsSystemVirtualMachines(t *testing.T) {
	sysRoot := t.TempDir()
	// Simulate /Library/Java/JavaVirtualMachines/temurin-21.jdk
	jdkBundle := filepath.Join(sysRoot, "temurin-21.jdk", "Contents", "Home")
	os.MkdirAll(jdkBundle, 0o755)
	content := `JAVA_VERSION="21.0.7"
IMPLEMENTOR="Eclipse Adoptium"
JAVA_RUNTIME_VERSION="21.0.7+6-LTS"
`
	os.WriteFile(filepath.Join(jdkBundle, "release"), []byte(content), 0o644)

	home := t.TempDir()
	opts := CollectOptions{
		Home:          home,
		SystemJVMRoot: sysRoot,
		UserJVMRoot:   filepath.Join(home, "nope"),
		BrewCellars:   []string{},
	}
	jdks, err := discoverJDKs(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(jdks) != 1 {
		t.Fatalf("got %d JDKs, want 1", len(jdks))
	}
	if jdks[0].Source != "system" {
		t.Errorf("Source = %q, want system", jdks[0].Source)
	}
	if jdks[0].VersionMajor != 21 {
		t.Errorf("VersionMajor = %d, want 21", jdks[0].VersionMajor)
	}
}

func TestDiscoverJDKsDedup(t *testing.T) {
	// Same physical path listed under two search roots should only appear once.
	home := t.TempDir()
	sdkBase := filepath.Join(home, ".sdkman", "candidates", "java")
	makeJDK(t, sdkBase, "21.0.6-tem", "21.0.6", "21.0.6+7-LTS", "Eclipse Adoptium")

	opts := CollectOptions{
		Home:          home,
		SystemJVMRoot: filepath.Join(home, "nope"),
		UserJVMRoot:   filepath.Join(home, "nope2"),
		BrewCellars:   []string{},
	}
	jdks1, _ := discoverJDKs(opts)
	jdks2, _ := discoverJDKs(opts)
	if len(jdks1) != len(jdks2) {
		t.Errorf("non-deterministic: %d vs %d", len(jdks1), len(jdks2))
	}
}
