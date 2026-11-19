package classify

import (
	"fmt"
	"path/filepath"
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
//  2. ai_agent  executable base name, argv[0] name, or the script/module a
//     node/bun/deno/python interpreter runs is in agentNames, or the script
//     lives in an agentPackages npm package. Matching is exact and
//     case-sensitive, so it also wins inside a bundle (Claude Code ships as
//     claude.app/Contents/MacOS/claude, Ollama.app runs Resources/ollama)
//     while desktop apps named "Claude" or "Ollama" stay apps.
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
//     or an Apple platform binary (except a non-root process from /bin/ or
//     /usr/bin/, which is a user-invoked tool). Linux: a uid 0 process under
//     /usr/lib/systemd/, /lib/systemd/, /usr/sbin/ or /sbin/.
//  7. cli       everything else.

// agentNames are AI coding agents and local model servers, matched exactly on
// a base name (never as a substring of arguments).
var agentNames = map[string]bool{
	"claude": true, "codex": true, "ollama": true, "aider": true, "cursor-agent": true,
	"gemini": true, "opencode": true, "goose": true, "amp": true, "crush": true,
	"copilot": true, "qwen": true,
}

// agentPackages are npm packages whose entry script an interpreter runs.
var agentPackages = map[string]bool{
	"@anthropic-ai/claude-code": true, "@openai/codex": true, "@google/gemini-cli": true,
	"opencode-ai": true, "@sourcegraph/amp": true, "@github/copilot": true,
	"@qwen-code/qwen-code": true, "@charmland/crush": true,
}

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
	exe       string // best-known executable path; may be empty
	argv0     string // argv[0] base name (process.Info.Command)
	cmdline   string
	runtime   Runtime
	loc       bundleLoc
	bundle    bundleInfo
	platform  bool // Apple platform binary per codesign
}

func (in roleInput) exeBase() string {
	if in.exe != "" {
		return filepath.Base(in.exe)
	}
	return strings.TrimPrefix(in.argv0, "-")
}

func decideRole(in roleInput) (Role, string) {
	if in.os == hostos.Linux && isKernelThread(in) {
		return RoleSystem, "kernel thread"
	}
	if ev := agentMatch(in); ev != "" {
		return RoleAIAgent, ev
	}
	base := in.exeBase()
	if terminalNames[strings.ToLower(base)] {
		return RoleTerminal, fmt.Sprintf("executable %s is a terminal", base)
	}
	if in.loc.appOwned() && in.bundle.isMainExecutable(in.loc.root, in.exe) && terminalNames[strings.ToLower(in.bundle.name)] {
		return RoleTerminal, fmt.Sprintf("main executable of terminal app %s", in.bundle.name)
	}
	if shellNames[strings.ToLower(base)] {
		return RoleShell, fmt.Sprintf("executable %s is a shell", base)
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
	if in.exe != "" && agentNames[filepath.Base(in.exe)] {
		return fmt.Sprintf("executable %s is an AI agent", filepath.Base(in.exe))
	}
	if name := strings.TrimPrefix(in.argv0, "-"); agentNames[name] {
		return fmt.Sprintf("argv[0] %s is an AI agent", name)
	}
	switch in.runtime {
	case RuntimeNode, RuntimeBun, RuntimeDeno, RuntimePython:
	default:
		return ""
	}
	script := interpreterScript(in.cmdline, in.runtime)
	if script == "" {
		return ""
	}
	if pkg := npmPackage(script); agentPackages[pkg] {
		return fmt.Sprintf("%s runs AI agent package %s", in.runtime, pkg)
	}
	name := filepath.Base(script)
	for _, ext := range []string{".js", ".mjs", ".cjs", ".ts", ".py"} {
		name = strings.TrimSuffix(name, ext)
	}
	if agentNames[name] {
		return fmt.Sprintf("%s runs AI agent script %s", in.runtime, name)
	}
	return ""
}

// interpreterScript returns the script path (or python -m module) an
// interpreter command line runs: the first non-flag argument after argv[0],
// skipping bun/deno subcommands.
func interpreterScript(cmdline string, rt Runtime) string {
	args := strings.Fields(cmdline)
	if len(args) < 2 {
		return ""
	}
	args = args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case rt == RuntimePython && a == "-m" && i+1 < len(args):
			return args[i+1]
		case rt == RuntimePython && a == "-c":
			return ""
		case strings.HasPrefix(a, "-"):
			continue
		case (rt == RuntimeBun || rt == RuntimeDeno) && (a == "run" || a == "x" || a == "exec"):
			continue
		}
		return a
	}
	return ""
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
		return "Apple platform binary"
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
