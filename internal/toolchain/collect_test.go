package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

// TestMain pins the host OS to Darwin so default BrewCellar / JVM-root
// selection and the xcrun-vs-make branch are host-independent in tests.
func TestMain(m *testing.M) {
	restore := hostos.SetForTest(hostos.Darwin)
	code := m.Run()
	restore()
	os.Exit(code)
}

func TestWithDefaultsLinux(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	o := withDefaults(CollectOptions{Home: "/home/u"})
	if len(o.BrewCellars) == 0 || o.BrewCellars[0] != "/home/linuxbrew/.linuxbrew/Cellar" {
		t.Errorf("BrewCellars = %v, want linuxbrew cellar first", o.BrewCellars)
	}
	if o.SystemJVMRoot != "/usr/lib/jvm" {
		t.Errorf("SystemJVMRoot = %q, want /usr/lib/jvm", o.SystemJVMRoot)
	}
	if o.UserJVMRoot != "" {
		t.Errorf("UserJVMRoot = %q, want empty on Linux", o.UserJVMRoot)
	}
}

func TestCollectIntegration(t *testing.T) {
	home := t.TempDir()

	// Plant a fake SDKMAN JDK.
	jdkDir := filepath.Join(home, ".sdkman", "candidates", "java", "21.0.6-tem")
	os.MkdirAll(jdkDir, 0o755)
	os.WriteFile(filepath.Join(jdkDir, "release"), []byte(`JAVA_VERSION="21.0.6"
IMPLEMENTOR="Eclipse Adoptium"
JAVA_RUNTIME_VERSION="21.0.6+7-LTS"
`), 0o644)

	// Plant a fake nvm Node.
	os.MkdirAll(filepath.Join(home, ".nvm", "versions", "node", "v22.1.0"), 0o755)

	// Plant a fake rustup toolchain.
	os.MkdirAll(filepath.Join(home, ".rustup", "toolchains", "stable-aarch64-apple-darwin"), 0o755)

	// Plant a fake jenv install and make it the active java shim.
	os.MkdirAll(filepath.Join(home, ".jenv", "bin"), 0o755)
	os.WriteFile(filepath.Join(home, ".jenv", "bin", "jenv"), []byte(""), 0o755)
	activeJava := filepath.Join(home, ".jenv", "shims", "java")

	// Stub brew to return empty.
	stub := func(name string, args ...string) ([]byte, error) {
		if name == "brew" && len(args) > 0 && args[0] == "info" {
			return []byte(`{"formulae":[]}`), nil
		}
		if name == "which" && len(args) == 1 && args[0] == "java" {
			return []byte(activeJava + "\n"), nil
		}
		return []byte{}, nil
	}

	tc := Collect(context.Background(), CollectOptions{
		Home:          home,
		BrewCellars:   []string{filepath.Join(home, "cellar")},
		SystemJVMRoot: filepath.Join(home, "nope"),
		UserJVMRoot:   filepath.Join(home, "nope2"),
		CmdRunner:     stub,
	})

	if len(tc.JDKs) != 1 {
		t.Errorf("JDKs: got %d, want 1", len(tc.JDKs))
	}
	if len(tc.Node) != 1 {
		t.Errorf("Node: got %d, want 1", len(tc.Node))
	}
	if len(tc.Rust) != 1 {
		t.Errorf("Rust: got %d, want 1", len(tc.Rust))
	}
	if tc.ActiveJVMManager != "jenv" {
		t.Errorf("ActiveJVMManager: got %q, want jenv", tc.ActiveJVMManager)
	}
}

func TestCollectJDKsOnly(t *testing.T) {
	home := t.TempDir()
	sdkBase := filepath.Join(home, ".sdkman", "candidates", "java")
	makeJDK(t, sdkBase, "21.0.6-tem", "21.0.6", "21.0.6+7-LTS", "Eclipse Adoptium")

	jdks := CollectJDKs(context.Background(), CollectOptions{
		Home:          home,
		BrewCellars:   []string{filepath.Join(home, "cellar")},
		SystemJVMRoot: filepath.Join(home, "nope"),
		UserJVMRoot:   filepath.Join(home, "nope2"),
	})
	if len(jdks) != 1 {
		t.Fatalf("JDKs: got %d, want 1", len(jdks))
	}
	if jdks[0].Source != "sdkman" {
		t.Errorf("Source = %q, want sdkman", jdks[0].Source)
	}
}
