package toolchain

import (
	"os"
	"strings"
	"testing"
)

func TestCollectEnvShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	snap := collectEnv()
	if snap.Shell != "zsh" {
		t.Errorf("Shell = %q, want zsh", snap.Shell)
	}
}

func TestCollectEnvPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/usr/local/bin:/usr/bin") // duplicate at end
	snap := collectEnv()
	if len(snap.PathDirs) != 2 {
		t.Errorf("PathDirs = %v, want 2 unique entries", snap.PathDirs)
	}
	if snap.PathDirs[0] != "/usr/bin" {
		t.Errorf("PathDirs[0] = %q, want /usr/bin", snap.PathDirs[0])
	}
}

func TestCollectEnvJavaHome(t *testing.T) {
	t.Setenv("JAVA_HOME", "/usr/local/opt/openjdk@21")
	snap := collectEnv()
	if snap.JavaHome != "/usr/local/opt/openjdk@21" {
		t.Errorf("JavaHome = %q", snap.JavaHome)
	}
}

func TestCollectEnvProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:8080")
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")
	snap := collectEnv()
	if snap.ProxyEnvVars["https_proxy"] != "http://proxy.example.com:8080" {
		t.Errorf("proxy not captured: %+v", snap.ProxyEnvVars)
	}
	if snap.ProxyEnvVars["no_proxy"] != "localhost,127.0.0.1" {
		t.Errorf("no_proxy not captured: %+v", snap.ProxyEnvVars)
	}
}

func TestCollectEnvEmptyProxy(t *testing.T) {
	// Unset all proxy vars to ensure they don't bleed from the test environment.
	for _, k := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"no_proxy", "NO_PROXY", "all_proxy", "ALL_PROXY"} {
		os.Unsetenv(k)
	}
	snap := collectEnv()
	if len(snap.ProxyEnvVars) != 0 {
		t.Errorf("expected empty proxy vars, got %+v", snap.ProxyEnvVars)
	}
}

func TestCollectEnvPathEmpty(t *testing.T) {
	t.Setenv("PATH", "")
	snap := collectEnv()
	if len(snap.PathDirs) != 0 {
		t.Errorf("expected empty PathDirs for empty PATH, got %v", snap.PathDirs)
	}
}

func TestCollectEnvShellBashPath(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	snap := collectEnv()
	if !strings.HasSuffix(snap.Shell, "bash") {
		t.Errorf("Shell = %q, want bash suffix", snap.Shell)
	}
}
