package toolchain

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// discoverNode enumerates Node.js installations from all common managers.
func discoverNode(opts CollectOptions) ([]RuntimeInstall, error) {
	active := activeVersion(opts.CmdRunner, "node")
	var out []RuntimeInstall

	// nvm
	for _, v := range listDirs(filepath.Join(opts.Home, ".nvm", "versions", "node")) {
		out = append(out, runtimeInstall("nvm", v, filepath.Join(opts.Home, ".nvm", "versions", "node", v, "bin", "node"), active))
	}
	// fnm
	for _, base := range []string{
		filepath.Join(opts.Home, ".fnm", "node-versions"),
		filepath.Join(opts.Home, ".local", "share", "fnm", "node-versions"),
	} {
		for _, v := range listDirs(base) {
			out = append(out, runtimeInstall("fnm", v, filepath.Join(base, v, "installation", "bin", "node"), active))
		}
	}
	// volta
	for _, v := range listDirs(filepath.Join(opts.Home, ".volta", "tools", "image", "node")) {
		out = append(out, runtimeInstall("volta", v, filepath.Join(opts.Home, ".volta", "tools", "image", "node", v, "bin", "node"), active))
	}
	// mise
	for _, v := range listDirs(filepath.Join(opts.Home, ".local", "share", "mise", "installs", "node")) {
		out = append(out, runtimeInstall("mise", v, filepath.Join(opts.Home, ".local", "share", "mise", "installs", "node", v, "bin", "node"), active))
	}
	// asdf
	for _, v := range listDirs(filepath.Join(opts.Home, ".asdf", "installs", "nodejs")) {
		out = append(out, runtimeInstall("asdf", v, filepath.Join(opts.Home, ".asdf", "installs", "nodejs", v, "bin", "node"), active))
	}
	// brew
	for _, cellar := range opts.BrewCellars {
		for _, pkg := range listDirs(cellar) {
			if !strings.HasPrefix(pkg, "node") {
				continue
			}
			for _, v := range listDirs(filepath.Join(cellar, pkg)) {
				out = append(out, runtimeInstall("brew", v, filepath.Join(cellar, pkg, v, "bin", "node"), active))
			}
		}
	}

	return out, nil
}

// discoverPython enumerates Python installations.
func discoverPython(opts CollectOptions) ([]RuntimeInstall, error) {
	active := activeVersion(opts.CmdRunner, "python3")
	var out []RuntimeInstall

	// system
	if _, err := os.Stat("/usr/bin/python3"); err == nil {
		out = append(out, RuntimeInstall{Version: "system", Source: "system", Path: "/usr/bin/python3",
			Active: strings.HasPrefix(active, "/usr/bin")})
	}
	// pyenv
	for _, v := range listDirs(filepath.Join(opts.Home, ".pyenv", "versions")) {
		out = append(out, runtimeInstall("pyenv", v, filepath.Join(opts.Home, ".pyenv", "versions", v, "bin", "python3"), active))
	}
	// uv
	for _, v := range listDirs(filepath.Join(opts.Home, ".local", "share", "uv", "python")) {
		out = append(out, runtimeInstall("uv", v, filepath.Join(opts.Home, ".local", "share", "uv", "python", v, "bin", "python3"), active))
	}
	// mise
	for _, v := range listDirs(filepath.Join(opts.Home, ".local", "share", "mise", "installs", "python")) {
		out = append(out, runtimeInstall("mise", v, filepath.Join(opts.Home, ".local", "share", "mise", "installs", "python", v, "bin", "python3"), active))
	}
	// asdf
	for _, v := range listDirs(filepath.Join(opts.Home, ".asdf", "installs", "python")) {
		out = append(out, runtimeInstall("asdf", v, filepath.Join(opts.Home, ".asdf", "installs", "python", v, "bin", "python3"), active))
	}
	// brew
	for _, cellar := range opts.BrewCellars {
		for _, pkg := range listDirs(cellar) {
			if !strings.HasPrefix(pkg, "python") {
				continue
			}
			for _, v := range listDirs(filepath.Join(cellar, pkg)) {
				out = append(out, runtimeInstall("brew", v, filepath.Join(cellar, pkg, v, "bin", "python3"), active))
			}
		}
	}

	return out, nil
}

// discoverGo enumerates Go installations.
func discoverGo(opts CollectOptions) ([]RuntimeInstall, error) {
	active := activeVersion(opts.CmdRunner, "go")
	var out []RuntimeInstall

	// system /usr/local/go
	if _, err := os.Stat("/usr/local/go/bin/go"); err == nil {
		out = append(out, RuntimeInstall{Version: "system", Source: "system", Path: "/usr/local/go/bin/go",
			Active: strings.HasPrefix(active, "/usr/local/go")})
	}
	// goenv
	for _, v := range listDirs(filepath.Join(opts.Home, ".goenv", "versions")) {
		out = append(out, runtimeInstall("goenv", v, filepath.Join(opts.Home, ".goenv", "versions", v, "bin", "go"), active))
	}
	// mise
	for _, v := range listDirs(filepath.Join(opts.Home, ".local", "share", "mise", "installs", "go")) {
		out = append(out, runtimeInstall("mise", v, filepath.Join(opts.Home, ".local", "share", "mise", "installs", "go", v, "bin", "go"), active))
	}
	// asdf
	for _, v := range listDirs(filepath.Join(opts.Home, ".asdf", "installs", "golang")) {
		out = append(out, runtimeInstall("asdf", v, filepath.Join(opts.Home, ".asdf", "installs", "golang", v, "packages", "pkg", "tool", "darwin_arm64", "go"), active))
	}
	// brew
	for _, cellar := range opts.BrewCellars {
		for _, pkg := range listDirs(cellar) {
			if pkg != "go" && !strings.HasPrefix(pkg, "go@") {
				continue
			}
			for _, v := range listDirs(filepath.Join(cellar, pkg)) {
				out = append(out, runtimeInstall("brew", v, filepath.Join(cellar, pkg, v, "bin", "go"), active))
			}
		}
	}

	return out, nil
}

// discoverRuby enumerates Ruby installations.
func discoverRuby(opts CollectOptions) ([]RuntimeInstall, error) {
	active := activeVersion(opts.CmdRunner, "ruby")
	var out []RuntimeInstall

	// rbenv
	for _, v := range listDirs(filepath.Join(opts.Home, ".rbenv", "versions")) {
		out = append(out, runtimeInstall("rbenv", v, filepath.Join(opts.Home, ".rbenv", "versions", v, "bin", "ruby"), active))
	}
	// mise
	for _, v := range listDirs(filepath.Join(opts.Home, ".local", "share", "mise", "installs", "ruby")) {
		out = append(out, runtimeInstall("mise", v, filepath.Join(opts.Home, ".local", "share", "mise", "installs", "ruby", v, "bin", "ruby"), active))
	}
	// asdf
	for _, v := range listDirs(filepath.Join(opts.Home, ".asdf", "installs", "ruby")) {
		out = append(out, runtimeInstall("asdf", v, filepath.Join(opts.Home, ".asdf", "installs", "ruby", v, "bin", "ruby"), active))
	}
	// brew
	for _, cellar := range opts.BrewCellars {
		for _, pkg := range listDirs(cellar) {
			if pkg != "ruby" && !strings.HasPrefix(pkg, "ruby@") {
				continue
			}
			for _, v := range listDirs(filepath.Join(cellar, pkg)) {
				out = append(out, runtimeInstall("brew", v, filepath.Join(cellar, pkg, v, "bin", "ruby"), active))
			}
		}
	}

	return out, nil
}

// discoverRust enumerates rustup-managed toolchains from ~/.rustup/toolchains/.
func discoverRust(home string) ([]RustToolchain, error) {
	base := filepath.Join(home, ".rustup", "toolchains")
	// default toolchain is the target of ~/.rustup/toolchains/stable-* symlink
	// or listed in ~/.rustup/settings.toml as default_toolchain.
	defName := readRustDefault(filepath.Join(home, ".rustup", "settings.toml"))

	var out []RustToolchain
	for _, name := range listDirs(base) {
		ch := rustChannel(name)
		out = append(out, RustToolchain{
			Toolchain: name,
			Channel:   ch,
			Default:   defName != "" && strings.HasPrefix(name, defName),
		})
	}
	return out, nil
}

// rustChannel extracts "stable", "beta", or "nightly" from a toolchain name
// like "stable-aarch64-apple-darwin".
func rustChannel(name string) string {
	for _, ch := range []string{"stable", "beta", "nightly"} {
		if strings.HasPrefix(name, ch) {
			return ch
		}
	}
	return "custom"
}

// readRustDefault reads `default_toolchain = "..."` from settings.toml.
func readRustDefault(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "default_toolchain" {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"`)
	}
	return ""
}

// listDirs returns immediate subdirectory names of dir. Returns nil on error.
func listDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			out = append(out, e.Name())
		}
	}
	return out
}

// activeVersion runs `which <bin>` through the injected runner and returns the
// resolved path.
func activeVersion(run CmdRunner, bin string) string {
	if run == nil {
		run = execRunner
	}
	out, err := run("which", bin)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runtimeInstall constructs a RuntimeInstall and marks it active when
// the resolved `which` path starts with the install's directory.
func runtimeInstall(source, version, binPath, activePath string) RuntimeInstall {
	dir := filepath.Dir(binPath) // trim /bin/node → .../version
	isActive := activePath != "" && strings.HasPrefix(activePath, dir)
	return RuntimeInstall{
		Version: version,
		Source:  source,
		Path:    binPath,
		Active:  isActive,
	}
}

// discoverJVMManagers returns the names of JVM version managers present in the
// user's home directory. Checks for sdkman, asdf, mise, and jenv by probing
// their canonical install paths.
func discoverJVMManagers(home string) []string {
	probes := []struct {
		name string
		path string
	}{
		{"sdkman", filepath.Join(home, ".sdkman", "bin", "sdkman-init.sh")},
		{"asdf", filepath.Join(home, ".asdf", "bin", "asdf")},
		{"mise", filepath.Join(home, ".local", "share", "mise")},
		{"jenv", filepath.Join(home, ".jenv", "bin", "jenv")},
	}
	var found []string
	for _, p := range probes {
		if _, err := os.Stat(p.path); err == nil {
			found = append(found, p.name)
		}
	}
	return found
}

func discoverActiveJVMManager(home string, run CmdRunner) string {
	java := activeVersion(run, "java")
	if java == "" {
		return ""
	}
	probes := []struct {
		name   string
		prefix string
	}{
		{"jenv", filepath.Join(home, ".jenv", "shims") + string(os.PathSeparator)},
		{"asdf", filepath.Join(home, ".asdf", "shims") + string(os.PathSeparator)},
		{"mise", filepath.Join(home, ".local", "share", "mise", "shims") + string(os.PathSeparator)},
		{"sdkman", filepath.Join(home, ".sdkman", "candidates", "java") + string(os.PathSeparator)},
	}
	for _, p := range probes {
		if strings.HasPrefix(java, p.prefix) {
			return p.name
		}
	}
	return ""
}

// discoverBuildTools finds installed Maven, Gradle, Bazel, Make, and CMake.
// Uses brew cellar presence (fast, no forking) as primary detection;
// falls back to version commands for tools not managed by brew.
func discoverBuildTools(opts CollectOptions) []BuildTool {
	var tools []BuildTool

	add := func(name, version, source string) {
		if version != "" {
			tools = append(tools, BuildTool{Name: name, Version: version, Source: source})
		}
	}

	// Maven — check brew cellar first, then mvn -version.
	if v := brewVersion(opts.BrewCellars, "maven"); v != "" {
		add("maven", v, "brew")
	} else if v := versionFromCmd(opts.CmdRunner, "mvn", "-version"); v != "" {
		add("maven", v, "system")
	}

	// Gradle — check brew cellar first, then gradle -version.
	if v := brewVersion(opts.BrewCellars, "gradle"); v != "" {
		add("gradle", v, "brew")
	} else if v := versionFromCmd(opts.CmdRunner, "gradle", "-version"); v != "" {
		add("gradle", v, "system")
	}

	// Bazel — check brew cellar first, then bazel version.
	if v := brewVersion(opts.BrewCellars, "bazel"); v != "" {
		add("bazel", v, "brew")
	} else if v := brewVersion(opts.BrewCellars, "bazelbuild/tap/bazel"); v != "" {
		add("bazel", v, "brew")
	} else if v := versionFromCmd(opts.CmdRunner, "bazel", "version"); v != "" {
		add("bazel", v, "system")
	}

	// Make — check brew cellar first, then Xcode command line tools.
	if v := brewVersion(opts.BrewCellars, "make"); v != "" {
		add("make", v, "brew")
	} else if v := versionFromCmd(opts.CmdRunner, "xcrun", "make", "--version"); v != "" {
		add("make", v, "system")
	}

	// CMake — check brew cellar first.
	if v := brewVersion(opts.BrewCellars, "cmake"); v != "" {
		add("cmake", v, "brew")
	} else if v := versionFromCmd(opts.CmdRunner, "cmake", "--version"); v != "" {
		add("cmake", v, "system")
	}

	return tools
}

// brewVersion returns the installed version of a formula by scanning
// the Cellar directory (no subprocess). Returns "" if not installed.
func brewVersion(cellars []string, formula string) string {
	base := filepath.Base(formula)
	for _, cellar := range cellars {
		dir := filepath.Join(cellar, base)
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			continue
		}
		// Sort and return the last (highest) version directory.
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			return names[len(names)-1]
		}
	}
	return ""
}

// versionFromCmd runs cmd args and extracts the first version-like token.
func versionFromCmd(run CmdRunner, cmd string, args ...string) string {
	out, err := run(cmd, args...)
	if err != nil {
		return ""
	}
	// Look for first token matching "X.Y.Z" or similar.
	re := regexp.MustCompile(`\d+\.\d+[\.\d]*`)
	m := re.Find(out)
	if m == nil {
		return ""
	}
	return string(m)
}
