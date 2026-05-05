package toolchain

import (
	"errors"
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

func TestDiscoverNodeMarksActiveFromInjectedRunner(t *testing.T) {
	home := t.TempDir()
	version := "v22.1.0"
	makeVersionDir(t, filepath.Join(home, ".nvm", "versions", "node"), version)
	activeNode := filepath.Join(home, ".nvm", "versions", "node", version, "bin", "node")

	opts := CollectOptions{
		Home:        home,
		BrewCellars: []string{},
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			if name == "which" && len(args) == 1 && args[0] == "node" {
				return []byte(activeNode + "\n"), nil
			}
			return nil, errors.New("unexpected command")
		},
	}
	installs, err := discoverNode(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 1 {
		t.Fatalf("got %d installs, want 1: %+v", len(installs), installs)
	}
	if !installs[0].Active {
		t.Fatalf("Active = false, want true for %s", activeNode)
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
		"stable-aarch64-apple-darwin":             "stable",
		"nightly-2025-03-01-aarch64-apple-darwin": "nightly",
		"beta-aarch64-apple-darwin":               "beta",
		"1.78.0-aarch64-apple-darwin":             "custom",
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

func TestDiscoverJVMManagersNone(t *testing.T) {
	home := t.TempDir()
	managers := discoverJVMManagers(home)
	if len(managers) != 0 {
		t.Errorf("expected no managers, got %v", managers)
	}
}

func TestDiscoverJVMManagersSDKMan(t *testing.T) {
	home := t.TempDir()
	sdkmanBin := filepath.Join(home, ".sdkman", "bin")
	if err := os.MkdirAll(sdkmanBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdkmanBin, "sdkman-init.sh"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	managers := discoverJVMManagers(home)
	if len(managers) != 1 || managers[0] != "sdkman" {
		t.Errorf("expected [sdkman], got %v", managers)
	}
}

func TestDiscoverJVMManagersMise(t *testing.T) {
	home := t.TempDir()
	misePath := filepath.Join(home, ".local", "share", "mise")
	if err := os.MkdirAll(misePath, 0o755); err != nil {
		t.Fatal(err)
	}
	managers := discoverJVMManagers(home)
	if len(managers) != 1 || managers[0] != "mise" {
		t.Errorf("expected [mise], got %v", managers)
	}
}

func TestDiscoverJVMManagersMultiple(t *testing.T) {
	home := t.TempDir()
	// Install both asdf and jenv.
	asdfBin := filepath.Join(home, ".asdf", "bin")
	if err := os.MkdirAll(asdfBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asdfBin, "asdf"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	jenvBin := filepath.Join(home, ".jenv", "bin")
	if err := os.MkdirAll(jenvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jenvBin, "jenv"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	managers := discoverJVMManagers(home)
	if len(managers) != 2 {
		t.Errorf("expected 2 managers, got %v", managers)
	}
	found := map[string]bool{}
	for _, m := range managers {
		found[m] = true
	}
	if !found["asdf"] || !found["jenv"] {
		t.Errorf("expected asdf and jenv in %v", managers)
	}
}

func TestDiscoverBuildToolsNone(t *testing.T) {
	home := t.TempDir()
	opts := CollectOptions{
		Home:        home,
		BrewCellars: []string{filepath.Join(home, "Cellar")},
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			return nil, errors.New("not found")
		},
	}
	tools := discoverBuildTools(opts)
	if len(tools) != 0 {
		t.Errorf("expected no tools, got %v", tools)
	}
}

func TestDiscoverBuildToolsMavenFromBrew(t *testing.T) {
	home := t.TempDir()
	cellar := filepath.Join(home, "Cellar")
	mavenDir := filepath.Join(cellar, "maven", "3.9.6")
	if err := os.MkdirAll(mavenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := CollectOptions{
		Home:        home,
		BrewCellars: []string{cellar},
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			return nil, errors.New("not found")
		},
	}
	tools := discoverBuildTools(opts)
	if len(tools) != 1 || tools[0].Name != "maven" {
		t.Errorf("expected [maven], got %v", tools)
	}
	if tools[0].Version != "3.9.6" {
		t.Errorf("version = %q, want 3.9.6", tools[0].Version)
	}
	if tools[0].Source != "brew" {
		t.Errorf("source = %q, want brew", tools[0].Source)
	}
}

func TestDiscoverBuildToolsMavenFromCmdUsesDocumentedFlag(t *testing.T) {
	home := t.TempDir()
	opts := CollectOptions{
		Home:        home,
		BrewCellars: []string{filepath.Join(home, "Cellar")},
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			if name == "mvn" && len(args) == 1 && args[0] == "-version" {
				return []byte("Apache Maven 3.9.6\n"), nil
			}
			return nil, errors.New("not found")
		},
	}
	tools := discoverBuildTools(opts)
	if len(tools) != 1 || tools[0].Name != "maven" {
		t.Errorf("expected [maven], got %v", tools)
	}
	if tools[0].Version != "3.9.6" {
		t.Errorf("version = %q, want 3.9.6", tools[0].Version)
	}
	if tools[0].Source != "system" {
		t.Errorf("source = %q, want system", tools[0].Source)
	}
}

func TestDiscoverBuildToolsGradleFromCmd(t *testing.T) {
	home := t.TempDir()
	opts := CollectOptions{
		Home:        home,
		BrewCellars: []string{filepath.Join(home, "Cellar")},
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			if name == "gradle" {
				return []byte("Gradle 8.5\n..."), nil
			}
			return nil, errors.New("not found")
		},
	}
	tools := discoverBuildTools(opts)
	if len(tools) != 1 || tools[0].Name != "gradle" {
		t.Errorf("expected [gradle], got %v", tools)
	}
	if tools[0].Version != "8.5" {
		t.Errorf("version = %q, want 8.5", tools[0].Version)
	}
}

func TestDiscoverBuildToolsMakeFromBrew(t *testing.T) {
	home := t.TempDir()
	cellar := filepath.Join(home, "Cellar")
	makeDir := filepath.Join(cellar, "make", "4.4.1")
	if err := os.MkdirAll(makeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := CollectOptions{
		Home:        home,
		BrewCellars: []string{cellar},
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			return nil, errors.New("not found")
		},
	}
	tools := discoverBuildTools(opts)
	if len(tools) != 1 || tools[0].Name != "make" {
		t.Errorf("expected [make], got %v", tools)
	}
	if tools[0].Version != "4.4.1" {
		t.Errorf("version = %q, want 4.4.1", tools[0].Version)
	}
	if tools[0].Source != "brew" {
		t.Errorf("source = %q, want brew", tools[0].Source)
	}
}

func TestDiscoverBuildToolsMakeFromXcrun(t *testing.T) {
	home := t.TempDir()
	opts := CollectOptions{
		Home:        home,
		BrewCellars: []string{filepath.Join(home, "Cellar")},
		CmdRunner: func(name string, args ...string) ([]byte, error) {
			if name == "xcrun" && len(args) == 2 && args[0] == "make" && args[1] == "--version" {
				return []byte("GNU Make 3.81\n"), nil
			}
			return nil, errors.New("not found")
		},
	}
	tools := discoverBuildTools(opts)
	if len(tools) != 1 || tools[0].Name != "make" {
		t.Errorf("expected [make], got %v", tools)
	}
	if tools[0].Version != "3.81" {
		t.Errorf("version = %q, want 3.81", tools[0].Version)
	}
	if tools[0].Source != "system" {
		t.Errorf("source = %q, want system", tools[0].Source)
	}
}
