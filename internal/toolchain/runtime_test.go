package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func makeVersionDir(t *testing.T, base, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, version), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverNodeFromNVM(t *testing.T) {
	home := t.TempDir()
	nvmBase := filepath.Join(home, ".nvm", "versions", "node")
	makeVersionDir(t, nvmBase, "v18.20.4")
	makeVersionDir(t, nvmBase, "v22.1.0")

	opts := CollectOptions{Home: home, BrewCellars: []string{}}
	installs, err := discoverNode(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 2 {
		t.Fatalf("got %d installs, want 2: %+v", len(installs), installs)
	}
	for _, i := range installs {
		if i.Source != "nvm" {
			t.Errorf("Source = %q, want nvm", i.Source)
		}
	}
}

func TestDiscoverNodeFromMultipleManagers(t *testing.T) {
	home := t.TempDir()
	makeVersionDir(t, filepath.Join(home, ".nvm", "versions", "node"), "v18.20.4")
	makeVersionDir(t, filepath.Join(home, ".fnm", "node-versions"), "v20.0.0")
	makeVersionDir(t, filepath.Join(home, ".local", "share", "mise", "installs", "node"), "22.1.0")

	opts := CollectOptions{Home: home, BrewCellars: []string{}}
	installs, err := discoverNode(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 3 {
		t.Fatalf("got %d installs, want 3: %+v", len(installs), installs)
	}

	sources := map[string]bool{}
	for _, i := range installs {
		sources[i.Source] = true
	}
	for _, want := range []string{"nvm", "fnm", "mise"} {
		if !sources[want] {
			t.Errorf("missing source %q", want)
		}
	}
}

func TestDiscoverRustToolchains(t *testing.T) {
	home := t.TempDir()
	rtBase := filepath.Join(home, ".rustup", "toolchains")
	makeVersionDir(t, rtBase, "stable-aarch64-apple-darwin")
	makeVersionDir(t, rtBase, "nightly-2025-03-01-aarch64-apple-darwin")
	makeVersionDir(t, rtBase, "beta-aarch64-apple-darwin")

	// Write a settings.toml pointing to stable.
	os.MkdirAll(filepath.Join(home, ".rustup"), 0o755)
	os.WriteFile(filepath.Join(home, ".rustup", "settings.toml"),
		[]byte(`default_toolchain = "stable-aarch64-apple-darwin"`+"\n"), 0o644)

	chains, err := discoverRust(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(chains) != 3 {
		t.Fatalf("got %d toolchains, want 3: %+v", len(chains), chains)
	}

	byChannel := map[string]RustToolchain{}
	for _, c := range chains {
		byChannel[c.Channel] = c
	}
	if _, ok := byChannel["stable"]; !ok {
		t.Error("missing stable toolchain")
	}
	if _, ok := byChannel["nightly"]; !ok {
		t.Error("missing nightly toolchain")
	}

	stable := byChannel["stable"]
	if !stable.Default {
		t.Error("stable should be marked as default")
	}
}

func TestRustChannel(t *testing.T) {
	cases := map[string]string{
		"stable-aarch64-apple-darwin":                 "stable",
		"nightly-2025-03-01-aarch64-apple-darwin":     "nightly",
		"beta-aarch64-apple-darwin":                   "beta",
		"1.78.0-aarch64-apple-darwin":                 "custom",
	}
	for in, want := range cases {
		if got := rustChannel(in); got != want {
			t.Errorf("rustChannel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverPythonFromPyenv(t *testing.T) {
	home := t.TempDir()
	pyenvBase := filepath.Join(home, ".pyenv", "versions")
	makeVersionDir(t, pyenvBase, "3.12.3")
	makeVersionDir(t, pyenvBase, "3.11.9")

	opts := CollectOptions{Home: home, BrewCellars: []string{}}
	installs, err := discoverPython(opts)
	if err != nil {
		t.Fatal(err)
	}

	count := 0
	for _, i := range installs {
		if i.Source == "pyenv" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("got %d pyenv installs, want 2", count)
	}
}

func TestDiscoverGoFromGoenv(t *testing.T) {
	home := t.TempDir()
	goenvBase := filepath.Join(home, ".goenv", "versions")
	makeVersionDir(t, goenvBase, "1.22.3")
	makeVersionDir(t, goenvBase, "1.23.0")

	opts := CollectOptions{Home: home, BrewCellars: []string{}}
	installs, err := discoverGo(opts)
	if err != nil {
		t.Fatal(err)
	}

	count := 0
	for _, i := range installs {
		if i.Source == "goenv" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("got %d goenv installs, want 2", count)
	}
}

func TestDiscoverRubyFromRbenv(t *testing.T) {
	home := t.TempDir()
	rbenvBase := filepath.Join(home, ".rbenv", "versions")
	makeVersionDir(t, rbenvBase, "3.3.0")

	opts := CollectOptions{Home: home, BrewCellars: []string{}}
	installs, err := discoverRuby(opts)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, i := range installs {
		if i.Source == "rbenv" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("got %d rbenv installs, want 1", count)
	}
}
