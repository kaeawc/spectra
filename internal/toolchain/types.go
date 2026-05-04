// Package toolchain catalogs every language runtime and package manager
// installed on the local machine. All discovery is read-only and local;
// no network calls, no brew update. See docs/inspection/toolchains.md.
package toolchain

// Toolchains is the aggregate result of all subsystem discoverers.
type Toolchains struct {
	Brew    BrewInventory    `json:"brew"`
	JDKs    []JDKInstall     `json:"jdks,omitempty"`
	Node    []RuntimeInstall `json:"node,omitempty"`
	Python  []RuntimeInstall `json:"python,omitempty"`
	Go      []RuntimeInstall `json:"go,omitempty"`
	Ruby    []RuntimeInstall `json:"ruby,omitempty"`
	Rust    []RustToolchain  `json:"rust,omitempty"`
	Env     EnvSnapshot      `json:"env"`
}

// BrewInventory holds installed Homebrew formulae, casks, and taps.
type BrewInventory struct {
	Formulae []BrewFormula `json:"formulae,omitempty"`
	Casks    []BrewCask    `json:"casks,omitempty"`
	Taps     []BrewTap     `json:"taps,omitempty"`
}

// BrewFormula is one installed Homebrew formula.
type BrewFormula struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	InstalledVia string `json:"installed_via,omitempty"` // "core", "tap", "cask-tied"
	Deprecated   bool   `json:"deprecated,omitempty"`
	Pinned       bool   `json:"pinned,omitempty"`
}

// BrewCask is one installed Homebrew cask.
type BrewCask struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// BrewTap is one configured Homebrew tap.
type BrewTap struct {
	Name string `json:"name"`
}

// JDKInstall represents one installed JDK found on the machine.
type JDKInstall struct {
	InstallID        string `json:"install_id"`
	Path             string `json:"path"`
	Source           string `json:"source"` // "system", "brew", "sdkman", "asdf", "mise", "jbr-toolbox", "manual"
	VersionMajor     int    `json:"version_major"`
	VersionMinor     int    `json:"version_minor"`
	VersionPatch     int    `json:"version_patch"`
	Vendor           string `json:"vendor,omitempty"`
	ReleaseString    string `json:"release_string,omitempty"`
	IsActiveJavaHome bool   `json:"is_active_java_home,omitempty"`
}

// RuntimeInstall is a version of Node, Python, Go, or Ruby found via
// a version manager or system install.
type RuntimeInstall struct {
	Version string `json:"version"`
	Source  string `json:"source"` // "system", "brew", "nvm", "pyenv", etc.
	Path    string `json:"path"`
	Active  bool   `json:"active,omitempty"`
}

// RustToolchain is one rustup-managed toolchain.
type RustToolchain struct {
	Toolchain string `json:"toolchain"`
	Channel   string `json:"channel"` // "stable", "beta", "nightly"
	Default   bool   `json:"default,omitempty"`
}

// EnvSnapshot captures the shell environment fields relevant to runtime
// resolution. Does NOT include full os.Environ — only named keys.
type EnvSnapshot struct {
	Shell        string            `json:"shell,omitempty"`
	PathDirs     []string          `json:"path_dirs,omitempty"`
	JavaHome     string            `json:"java_home,omitempty"`
	GoPath       string            `json:"go_path,omitempty"`
	GoRoot       string            `json:"go_root,omitempty"`
	NpmPrefix    string            `json:"npm_prefix,omitempty"`
	PnpmHome     string            `json:"pnpm_home,omitempty"`
	ProxyEnvVars map[string]string `json:"proxy_env_vars,omitempty"`
}
