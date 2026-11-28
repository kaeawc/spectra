//! Role precedence (Go's `role.go`): the first rule that matches wins.
//!
//!  1. system    Linux kernel thread (bracketed comm, pid or ppid 2).
//!  2. ai_agent  see [`AGENT_RULES`].
//!  3. terminal  executable base name is a terminal, or the process is the main executable of an
//!     app whose bundle name is.
//!  4. shell     executable base name is a shell.
//!  5. app / app_helper  inside an `.app` (macOS): its CFBundleExecutable is app, anything else
//!     is app_helper. Bundles under /System/Library/ or /Library/Apple/ are OS plumbing and fall
//!     to system. A `.app` reached through a `.framework` or a tool under
//!     `<app>/Contents/Developer/` does not count.
//!  6. system    macOS: under /System/, /usr/libexec/, /usr/sbin/ or /sbin/, or a validated
//!     Apple platform binary (except a non-root process from /bin/ or /usr/bin/). Linux: a uid 0
//!     process under /usr/lib/systemd/, /lib/systemd/, /usr/sbin/ or /sbin/.
//!  7. cli       everything else.
//!
//! Every path rule uses the kernel-reported executable, never argv[0], which any process can set
//! (`exec -a`). When the executable is not visible, rules 2-4 may match on the argv[0] name with
//! evidence marked unverified, and rules 5-6 cannot match.

use spectra_base::hostos::Kind;

use super::bundle::{BundleInfo, BundleLoc};
use super::gopath::{base, dir};
use super::{Role, Runtime, shell_name};

/// One AI coding agent or local model server, keyed by executable (or entry-script) base name,
/// matched exactly. An ambiguous name needs one confirming signal: the Go main module, the npm
/// package of the script, the owning app's bundle id, or an install-path substring.
pub struct AgentRule {
    pub ambiguous: bool,
    pub modules: &'static [&'static str],
    pub packages: &'static [&'static str],
    pub bundle_ids: &'static [&'static str],
    pub paths: &'static [&'static str],
}

const fn rule(ambiguous: bool, packages: &'static [&'static str]) -> AgentRule {
    AgentRule { ambiguous, modules: &[], packages, bundle_ids: &[], paths: &[] }
}

/// Go's `agentRules`.
pub const AGENT_RULES: &[(&str, AgentRule)] = &[
    ("claude", rule(false, &["@anthropic-ai/claude-code"])),
    ("codex", rule(false, &["@openai/codex"])),
    ("ollama", rule(false, &[])),
    ("aider", rule(false, &[])),
    ("cursor-agent", rule(false, &[])),
    ("opencode", rule(false, &["opencode-ai"])),
    ("gemini", rule(true, &["@google/gemini-cli"])),
    ("copilot", rule(true, &["@github/copilot"])),
    ("qwen", rule(true, &["@qwen-code/qwen-code"])),
    ("amp", AgentRule { paths: &["/.amp/bin/"], ..rule(true, &["@sourcegraph/amp"]) }),
    ("crush", AgentRule { modules: &["github.com/charmbracelet/crush"], ..rule(true, &["@charmland/crush"]) }),
    // ~/.local/bin/goose is also where pressly/goose, the database migration tool, installs, so
    // only the desktop app's bundle id confirms Block's.
    ("goose", AgentRule { bundle_ids: &["com.block.goose"], ..rule(true, &[]) }),
];

fn agent_rule(name: &str) -> Option<&'static AgentRule> {
    AGENT_RULES.iter().find(|(n, _)| *n == name).map(|(_, r)| r)
}

/// The agent name owning npm package `pkg` (Go's `agentPackages`).
fn agent_package(pkg: &str) -> Option<&'static str> {
    AGENT_RULES.iter().find(|(_, r)| r.packages.contains(&pkg)).map(|(n, _)| *n)
}

/// Terminal emulators and multiplexers (lowercased).
const TERMINAL_NAMES: &[&str] = &[
    "terminal",
    "iterm",
    "iterm2",
    "ghostty",
    "kitty",
    "alacritty",
    "wezterm",
    "wezterm-gui",
    "warp",
    "hyper",
    "tabby",
    "rio",
    "tmux",
    "screen",
    "zellij",
    "gnome-terminal-server",
    "konsole",
    "xterm",
    "foot",
    "terminator",
    "tilix",
];

fn terminal_name(name: &str) -> bool {
    TERMINAL_NAMES.contains(&name.to_lowercase().as_str())
}

/// Marks evidence drawn from argv[0] instead of the executable.
pub const UNVERIFIED: &str = " (from argv[0], unverified)";

/// What [`decide_role`] reads (Go's `roleInput`).
#[derive(Clone, Default)]
pub struct RoleInput {
    pub os: Kind,
    pub pid: i64,
    pub ppid: i64,
    pub uid: i64,
    /// Kernel-reported executable path; may be empty.
    pub exe: String,
    /// argv[0] base name.
    pub argv0: String,
    pub cmdline: String,
    pub runtime: Runtime,
    pub loc: BundleLoc,
    pub bundle: BundleInfo,
    /// Validated Apple platform binary.
    pub platform: bool,
    /// Go main module of the executable, when known.
    pub go_module: String,
    /// The interpreter's script with symlinks resolved.
    pub script: String,
}

impl RoleInput {
    fn exe_base(&self) -> (String, &'static str) {
        if !self.exe.is_empty() {
            return (base(&self.exe), "");
        }
        (self.argv0.strip_prefix('-').unwrap_or(&self.argv0).to_string(), UNVERIFIED)
    }
}

/// Go's `decideRole`.
pub fn decide_role(r: &RoleInput) -> (Role, String) {
    if r.os == Kind::Linux && is_kernel_thread(&r.exe, r.pid, r.ppid, &r.cmdline) {
        return (Role::System, "kernel thread".into());
    }
    if let Some(ev) = agent_match(r) {
        return (Role::AiAgent, ev);
    }
    let (b, note) = r.exe_base();
    if terminal_name(&b) {
        return (Role::Terminal, format!("executable {b} is a terminal{note}"));
    }
    if r.loc.app_owned() && r.bundle.is_main_executable(&r.loc.root, &r.exe) && terminal_name(&r.bundle.name) {
        return (Role::Terminal, format!("main executable of terminal app {}", r.bundle.name));
    }
    if shell_name(&b.to_lowercase()) {
        return (Role::Shell, format!("executable {b} is a shell{note}"));
    }
    if r.loc.app_owned() {
        return app_role(r);
    }
    if let Some(ev) = system_match(r) {
        return (Role::System, ev);
    }
    (Role::Cli, "no app, system, shell, terminal or agent signal".into())
}

fn app_role(r: &RoleInput) -> (Role, String) {
    let root = &r.loc.root;
    if root.starts_with("/System/Library/") || root.starts_with("/Library/Apple/") {
        return (Role::System, format!("inside OS bundle {root}"));
    }
    if r.bundle.is_main_executable(root, &r.exe) {
        return (Role::App, format!("main executable of {}", base(root)));
    }
    (Role::AppHelper, format!("non-main executable inside {}", base(root)))
}

/// Go's `isKernelThread`.
pub fn is_kernel_thread(exe: &str, pid: i64, ppid: i64, cmdline: &str) -> bool {
    exe.is_empty() && cmdline.starts_with('[') && cmdline.ends_with(']') && (pid == 2 || ppid == 2)
}

fn agent_match(r: &RoleInput) -> Option<String> {
    if !r.exe.is_empty() {
        if let Some(ev) = exe_agent(r) {
            return Some(ev);
        }
    } else {
        let name = r.argv0.strip_prefix('-').unwrap_or(&r.argv0);
        if distinct_agent(name) {
            return Some(format!("argv[0] {name} is an AI agent{UNVERIFIED}"));
        }
    }
    script_agent(r)
}

/// A known agent whose name decides on its own.
fn distinct_agent(name: &str) -> bool {
    agent_rule(name).is_some_and(|r| !r.ambiguous)
}

fn exe_agent(r: &RoleInput) -> Option<String> {
    if claude_native(&r.exe) {
        return Some("executable is a Claude Code version file under claude/versions".into());
    }
    let name = base(&r.exe);
    let rule = agent_rule(&name)?;
    if !rule.ambiguous {
        return Some(format!("executable {name} is an AI agent"));
    }
    confirm(rule, r).map(|why| format!("executable {name} is an AI agent ({why})"))
}

/// Go's `versionName`: `^[0-9]+(\.[0-9]+)+([-+][0-9A-Za-z.-]+)?$`.
fn version_name(s: &str) -> bool {
    let b = s.as_bytes();
    let digits = |i: usize| -> usize { b[i..].iter().take_while(|c| c.is_ascii_digit()).count() };
    let mut i = digits(0);
    if i == 0 {
        return false;
    }
    let mut groups = 0;
    while i < b.len() && b[i] == b'.' {
        let n = digits(i + 1);
        if n == 0 {
            break;
        }
        i += 1 + n;
        groups += 1;
    }
    if groups == 0 {
        return false;
    }
    if i == b.len() {
        return true;
    }
    if b[i] != b'-' && b[i] != b'+' {
        return false;
    }
    let tail = &b[i + 1..];
    !tail.is_empty() && tail.iter().all(|c| c.is_ascii_alphanumeric() || *c == b'.' || *c == b'-')
}

/// `.../claude/versions/<version>`, the native installer layout.
fn claude_native(exe: &str) -> bool {
    let d = dir(exe);
    version_name(&base(exe)) && base(&d) == "versions" && base(&dir(&d)) == "claude"
}

fn confirm(rule: &AgentRule, r: &RoleInput) -> Option<String> {
    if let Some(m) = rule.modules.iter().find(|m| r.go_module == **m) {
        return Some(format!("Go module {m}"));
    }
    if let Some(id) = rule.bundle_ids.iter().find(|id| r.bundle.bundle_id == **id) {
        return Some(format!("bundle {id}"));
    }
    rule.paths.iter().find(|p| r.exe.contains(**p)).map(|p| format!("installed under {p}"))
}

fn script_agent(r: &RoleInput) -> Option<String> {
    if r.script.is_empty() {
        return None;
    }
    let pkg = npm_package(&r.script);
    if agent_package(&pkg).is_some() {
        return Some(format!("{} runs AI agent package {pkg}", r.runtime));
    }
    let mut name = base(&r.script);
    for ext in [".js", ".mjs", ".cjs", ".ts", ".py"] {
        if let Some(n) = name.strip_suffix(ext) {
            name = n.to_string();
        }
    }
    distinct_agent(&name).then(|| format!("{} runs AI agent script {name}", r.runtime))
}

/// Options of each interpreter that consume the next argument.
fn value_flag(rt: Runtime, a: &str) -> bool {
    let flags: &[&str] = match rt {
        Runtime::Node => &[
            "-r",
            "--require",
            "--import",
            "--loader",
            "--experimental-loader",
            "-C",
            "--conditions",
            "--input-type",
            "--title",
            "--inspect-port",
            "--env-file",
            "--watch-path",
            "--redirect-warnings",
            "--icu-data-dir",
            "--openssl-config",
            "--diagnostic-dir",
            "--report-dir",
            "--cpu-prof-dir",
            "--heap-prof-dir",
            "--stack-trace-limit",
        ],
        Runtime::Bun => &[
            "-r",
            "--preload",
            "-c",
            "--config",
            "--cwd",
            "--env-file",
            "-d",
            "--define",
            "-l",
            "--loader",
            "--tsconfig-override",
            "--port",
            "--main-fields",
            "--conditions",
            "--extension-order",
        ],
        // --inspect[-brk|-wait] take a value only as --flag=value.
        Runtime::Deno => &["-c", "--config", "--import-map", "--lock", "--cert", "--location", "--seed", "--env-file", "--ext"],
        Runtime::Python => &["-W", "-X", "--check-hash-based-pycs"],
        _ => &[],
    };
    flags.contains(&a)
}

/// Options after which no script file runs.
fn no_script(rt: Runtime, a: &str) -> bool {
    let flags: &[&str] = match rt {
        Runtime::Node | Runtime::Bun => &["-e", "--eval", "-p", "--print"],
        Runtime::Python => &["-c"],
        _ => &[],
    };
    flags.contains(&a)
}

/// Go's `interpreterScript`: [`interpreter_args_script`] over the whitespace-split command line.
pub fn interpreter_script(cmdline: &str, rt: Runtime) -> String {
    let args: Vec<String> = cmdline.split_whitespace().map(String::from).collect();
    interpreter_args_script(&args, rt)
}

/// The script path (or `python -m` module) an interpreter's argv runs: the first argument after
/// argv[0] that is not an option or an option's value, skipping bun/deno subcommands.
pub fn interpreter_args_script(args: &[String], rt: Runtime) -> String {
    if args.len() < 2 {
        return String::new();
    }
    let args = &args[1..];
    let mut i = 0;
    while i < args.len() {
        let a = args[i].as_str();
        if a == "--" || rt == Runtime::Python && a == "-m" {
            return args.get(i + 1).cloned().unwrap_or_default();
        } else if no_script(rt, a) {
            return String::new();
        } else if value_flag(rt, a) {
            i += 1;
        } else if !(a.starts_with('-') || runner_subcommand(rt, a)) {
            return a.to_string();
        }
        i += 1;
    }
    String::new()
}

fn runner_subcommand(rt: Runtime, a: &str) -> bool {
    (rt == Runtime::Bun || rt == Runtime::Deno) && (a == "run" || a == "x" || a == "exec")
}

/// The npm package a script path belongs to (`.../node_modules/@scope/pkg/cli.js` is
/// `@scope/pkg`).
pub fn npm_package(script: &str) -> String {
    let Some(idx) = script.rfind("node_modules/") else { return String::new() };
    let parts: Vec<&str> = script[idx + "node_modules/".len()..].split('/').collect();
    if parts[0].starts_with('@') && parts.len() > 1 {
        return format!("{}/{}", parts[0], parts[1]);
    }
    parts[0].to_string()
}

fn has_any_prefix(s: &str, prefixes: &[&str]) -> bool {
    prefixes.iter().any(|p| s.starts_with(p))
}

fn system_match(r: &RoleInput) -> Option<String> {
    let exe = &r.exe;
    if exe.is_empty() {
        return None;
    }
    if r.os == Kind::Linux {
        return (r.uid == 0 && has_any_prefix(exe, &["/usr/lib/systemd/", "/lib/systemd/", "/usr/sbin/", "/sbin/"]))
            .then(|| "root daemon under a system directory".into());
    }
    if has_any_prefix(exe, &["/System/", "/usr/libexec/", "/usr/sbin/", "/sbin/"]) {
        return Some("executable under a system directory".into());
    }
    (r.platform && (r.uid == 0 || !has_any_prefix(exe, &["/bin/", "/usr/bin/"]))).then(|| "validated Apple platform binary".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn version_name_matches_go_regexp() {
        for (s, want) in [
            ("2.1.220", true),
            ("1.0", true),
            ("1.2.3-beta.1", true),
            ("1.2+build", true),
            ("1", false),
            ("1.", false),
            ("a.1", false),
            ("1.2-", false),
            ("1.2_x", false),
            ("1.2.x", false),
        ] {
            assert_eq!(version_name(s), want, "{s}");
        }
    }
}
