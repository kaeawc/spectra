package classify

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
)

// Role is what a process is for, from the user's point of view.
type Role string

const (
	RoleApp       Role = "app"
	RoleAppHelper Role = "app_helper"
	RoleSystem    Role = "system"
	RoleShell     Role = "shell"
	RoleTerminal  Role = "terminal"
	RoleAIAgent   Role = "ai_agent"
	RoleCLI       Role = "cli"
)

// Role precedence: the first rule that matches wins.
//
//  1. system    Linux kernel thread (bracketed comm, pid or ppid 2).
//  2. ai_agent  see agentRules below.
//  3. terminal  executable base name is in terminalNames, or the process is
//     the main executable of an app whose bundle name is.
//  4. shell     executable base name is in shellNames.
//  5. app / app_helper  inside an .app (macOS): its CFBundleExecutable is
//     app, anything else (Helper apps, XPC services, bundled tools) is
//     app_helper. Bundles under /System/Library/ or /Library/Apple/ are OS
//     plumbing and fall to system instead. A .app reached through a
//     .framework (Python.framework/.../Python.app) or a tool under
//     <app>/Contents/Developer/ (Xcode's toolchain) does not count.
//  6. system    macOS: under /System/, /usr/libexec/, /usr/sbin/ or /sbin/,
//     or a validated Apple platform binary (except a non-root process from
//     /bin/ or /usr/bin/, which is a user-invoked tool). Linux: a uid 0
//     process under /usr/lib/systemd/, /lib/systemd/, /usr/sbin/ or /sbin/.
//  7. cli       everything else.
//
// Every path rule uses the kernel-reported executable, never argv[0], which
// any process can set (exec -a). When the executable is not visible, rules
// 2-4 may match on the argv[0] name with evidence marked unverified, and
// rules 5-6 cannot match.

// agentRule describes one AI coding agent or local model server, keyed by
// executable (or entry-script) base name, matched exactly and
// case-sensitively so desktop apps named "Claude" or "Ollama" stay apps.
//
// The name alone decides only for distinctive names. An ambiguous name, one
// that unrelated tools also use (pressly/goose migrations, other "amp" or
// "gemini" binaries), needs one confirming signal:
//   - modules:   the binary's Go main module path (from build info)
//   - packages:  the npm package of the script an interpreter runs
//   - bundleIDs: the CFBundleIdentifier of the owning .app
//   - paths:     a substring of the executable path at a known install location
//
// Without confirmation an ambiguous name falls through to the other roles
// (normally cli). An interpreter running a script from any listed package is
// an agent for every rule. Claude Code's native installer runs a file named
// after its version inside a claude directory
// (~/.local/share/claude/versions/2.1.220); that is matched by path shape.
type agentRule struct {
	ambiguous bool
	modules   []string
	packages  []string
	bundleIDs []string
	paths     []string
}

var agentRules = map[string]agentRule{
	"claude":       {packages: []string{"@anthropic-ai/claude-code"}},
	"codex":        {packages: []string{"@openai/codex"}},
	"ollama":       {},
	"aider":        {},
	"cursor-agent": {},
	"opencode":     {packages: []string{"opencode-ai"}},
	"gemini":       {ambiguous: true, packages: []string{"@google/gemini-cli"}},
	"copilot":      {ambiguous: true, packages: []string{"@github/copilot"}},
	"qwen":         {ambiguous: true, packages: []string{"@qwen-code/qwen-code"}},
	"amp":          {ambiguous: true, packages: []string{"@sourcegraph/amp"}, paths: []string{"/.amp/bin/"}},
	"crush":        {ambiguous: true, packages: []string{"@charmland/crush"}, modules: []string{"github.com/charmbracelet/crush"}},
	// ~/.local/bin/goose is also where pressly/goose, the database migration
	// tool, installs, so only the desktop app's bundle id confirms Block's.
	"goose": {ambiguous: true, bundleIDs: []string{"com.block.goose"}},
}

// agentPackages maps each listed npm package to its agent name.
var agentPackages = func() map[string]string {
	m := map[string]string{}
	for name, r := range agentRules {
		for _, p := range r.packages {
			m[p] = name
		}
	}
	return m
}()

var versionName = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+([-+][0-9A-Za-z.-]+)?$`)

// terminalNames are terminal emulators and multiplexers (lowercased).
var terminalNames = map[string]bool{
	"terminal": true, "iterm": true, "iterm2": true, "ghostty": true, "kitty": true,
	"alacritty": true, "wezterm": true, "wezterm-gui": true, "warp": true, "hyper": true,
	"tabby": true, "rio": true, "tmux": true, "screen": true, "zellij": true,
	"gnome-terminal-server": true, "konsole": true, "xterm": true, "foot": true,
	"terminator": true, "tilix": true,
}

type roleInput struct {
	os        hostos.Kind
	pid, ppid int
	uid       int
	exe       string // kernel-reported executable path; may be empty
	argv0     string // argv[0] base name (process.Info.Command)
	cmdline   string
	runtime   Runtime
	loc       bundleLoc
	bundle    bundleInfo
	platform  bool   // validated Apple platform binary
	goModule  string // Go main module of the executable, when known
	// script is the interpreter's script with symlinks resolved; empty when
	// the runtime is not an interpreter or none was found.
	script string
}

// unverified marks evidence drawn from argv[0] instead of the executable.
const unverified = " (from argv[0], unverified)"

func (in roleInput) exeBase() (string, string) {
	if in.exe != "" {
		return filepath.Base(in.exe), ""
	}
	return strings.TrimPrefix(in.argv0, "-"), unverified
}

func decideRole(in roleInput) (Role, string) {
	if in.os == hostos.Linux && isKernelThread(in) {
		return RoleSystem, "kernel thread"
	}
	if ev := agentMatch(in); ev != "" {
		return RoleAIAgent, ev
	}
	base, note := in.exeBase()
	if terminalNames[strings.ToLower(base)] {
		return RoleTerminal, fmt.Sprintf("executable %s is a terminal%s", base, note)
	}
	if in.loc.appOwned() && in.bundle.isMainExecutable(in.loc.root, in.exe) && terminalNames[strings.ToLower(in.bundle.name)] {
		return RoleTerminal, fmt.Sprintf("main executable of terminal app %s", in.bundle.name)
	}
	if shellNames[strings.ToLower(base)] {
		return RoleShell, fmt.Sprintf("executable %s is a shell%s", base, note)
	}
	if in.loc.appOwned() {
		return appRole(in)
	}
	if ev := systemMatch(in); ev != "" {
		return RoleSystem, ev
	}
	return RoleCLI, "no app, system, shell, terminal or agent signal"
}

func appRole(in roleInput) (Role, string) {
	root := in.loc.root
	if strings.HasPrefix(root, "/System/Library/") || strings.HasPrefix(root, "/Library/Apple/") {
		return RoleSystem, fmt.Sprintf("inside OS bundle %s", root)
	}
	if in.bundle.isMainExecutable(root, in.exe) {
		return RoleApp, fmt.Sprintf("main executable of %s", filepath.Base(root))
	}
	return RoleAppHelper, fmt.Sprintf("non-main executable inside %s", filepath.Base(root))
}

func isKernelThread(in roleInput) bool {
	return in.exe == "" && strings.HasPrefix(in.cmdline, "[") && strings.HasSuffix(in.cmdline, "]") &&
		(in.pid == 2 || in.ppid == 2)
}

func agentMatch(in roleInput) string {
	if in.exe != "" {
		if ev := exeAgent(in); ev != "" {
			return ev
		}
	} else if name := strings.TrimPrefix(in.argv0, "-"); distinctAgent(name) {
		return fmt.Sprintf("argv[0] %s is an AI agent%s", name, unverified)
	}
	return scriptAgent(in)
}

// distinctAgent reports a known agent whose name decides on its own.
func distinctAgent(name string) bool {
	r, ok := agentRules[name]
	return ok && !r.ambiguous
}

func exeAgent(in roleInput) string {
	if claudeNative(in.exe) {
		return "executable is a Claude Code version file under claude/versions"
	}
	name := filepath.Base(in.exe)
	r, ok := agentRules[name]
	if !ok {
		return ""
	}
	if !r.ambiguous {
		return fmt.Sprintf("executable %s is an AI agent", name)
	}
	if why := r.confirm(in); why != "" {
		return fmt.Sprintf("executable %s is an AI agent (%s)", name, why)
	}
	return ""
}

// claudeNative matches .../claude/versions/<version>, the native installer
// layout whose executable has no "claude" base name.
func claudeNative(exe string) bool {
	dir := filepath.Dir(exe)
	return versionName.MatchString(filepath.Base(exe)) && filepath.Base(dir) == "versions" &&
		filepath.Base(filepath.Dir(dir)) == "claude"
}

func (r agentRule) confirm(in roleInput) string {
	for _, m := range r.modules {
		if in.goModule == m {
			return "Go module " + m
		}
	}
	for _, id := range r.bundleIDs {
		if in.bundle.bundleID == id {
			return "bundle " + id
		}
	}
	for _, p := range r.paths {
		if strings.Contains(in.exe, p) {
			return "installed under " + p
		}
	}
	return ""
}

func scriptAgent(in roleInput) string {
	if in.script == "" {
		return ""
	}
	if pkg := npmPackage(in.script); agentPackages[pkg] != "" {
		return fmt.Sprintf("%s runs AI agent package %s", in.runtime, pkg)
	}
	name := filepath.Base(in.script)
	for _, ext := range []string{".js", ".mjs", ".cjs", ".ts", ".py"} {
		name = strings.TrimSuffix(name, ext)
	}
	if distinctAgent(name) {
		return fmt.Sprintf("%s runs AI agent script %s", in.runtime, name)
	}
	return ""
}

// interpreterValueFlags are the options of each interpreter that consume the
// next argument, so that argument is not taken for the script.
var interpreterValueFlags = map[Runtime]map[string]bool{
	RuntimeNode: setOf("-r", "--require", "--import", "--loader", "--experimental-loader",
		"-C", "--conditions", "--input-type", "--title", "--inspect-port", "--env-file",
		"--watch-path", "--redirect-warnings", "--icu-data-dir", "--openssl-config",
		"--diagnostic-dir", "--report-dir", "--cpu-prof-dir", "--heap-prof-dir", "--stack-trace-limit"),
	RuntimeBun: setOf("-r", "--preload", "-c", "--config", "--cwd", "--env-file", "-d", "--define",
		"-l", "--loader", "--tsconfig-override", "--port", "--main-fields", "--conditions", "--extension-order"),
	RuntimeDeno: setOf("-c", "--config", "--import-map", "--lock", "--cert", "--location", "--seed",
		"--env-file", "--ext"), // --inspect[-brk|-wait] take a value only as --flag=value
	RuntimePython: setOf("-W", "-X", "--check-hash-based-pycs"),
}

// interpreterNoScript are options after which no script file runs.
var interpreterNoScript = map[Runtime]map[string]bool{
	RuntimeNode:   setOf("-e", "--eval", "-p", "--print"),
	RuntimeBun:    setOf("-e", "--eval", "-p", "--print"),
	RuntimePython: setOf("-c"),
}

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// interpreterScript returns the script path (or python -m module) an
// interpreter command line runs: the first argument after argv[0] that is
// not an option or an option's value, skipping bun/deno subcommands.
func interpreterScript(cmdline string, rt Runtime) string {
	args := strings.Fields(cmdline)
	if len(args) < 2 {
		return ""
	}
	args = args[1:]
	values := interpreterValueFlags[rt]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--" || rt == RuntimePython && a == "-m":
			// The next argument is the script (or module), whatever it looks like.
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case interpreterNoScript[rt][a]:
			return ""
		case values[a]:
			i++
		case strings.HasPrefix(a, "-") || runnerSubcommand(rt, a):
		default:
			return a
		}
	}
	return ""
}

func runnerSubcommand(rt Runtime, a string) bool {
	return (rt == RuntimeBun || rt == RuntimeDeno) && (a == "run" || a == "x" || a == "exec")
}

// npmPackage returns the package a script path belongs to
// (".../node_modules/@scope/pkg/cli.js" -> "@scope/pkg").
func npmPackage(script string) string {
	idx := strings.LastIndex(script, "node_modules/")
	if idx < 0 {
		return ""
	}
	parts := strings.Split(script[idx+len("node_modules/"):], "/")
	if strings.HasPrefix(parts[0], "@") && len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func systemMatch(in roleInput) string {
	exe := in.exe
	if exe == "" {
		return ""
	}
	if in.os == hostos.Linux {
		if in.uid == 0 && hasAnyPrefix(exe, "/usr/lib/systemd/", "/lib/systemd/", "/usr/sbin/", "/sbin/") {
			return "root daemon under a system directory"
		}
		return ""
	}
	if hasAnyPrefix(exe, "/System/", "/usr/libexec/", "/usr/sbin/", "/sbin/") {
		return "executable under a system directory"
	}
	if in.platform && (in.uid == 0 || !hasAnyPrefix(exe, "/bin/", "/usr/bin/")) {
		return "validated Apple platform binary"
	}
	return ""
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
