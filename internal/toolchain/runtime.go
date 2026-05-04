package toolchain

import (
	"os"
	"path/filepath"
	"strings"
)

// discoverNode enumerates Node.js installations from all common managers.
func discoverNode(opts CollectOptions) ([]RuntimeInstall, error) {
	active := activeVersion("node")
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
	active := activeVersion("python3")
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
	active := activeVersion("go")
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
	active := activeVersion("ruby")
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

// activeVersion runs `which <bin>` and returns the resolved path.
func activeVersion(bin string) string {
	out, err := execRunner("which", bin)
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
