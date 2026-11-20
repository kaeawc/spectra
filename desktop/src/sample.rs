//! Sample data for headless renders and tests: three JVMs (one leaking, one tight on heap, one
//! healthy) and a machine's worth of processes covering every role, most runtimes and every
//! signing status. Each scene opens a different screen.

use crate::app::App;
use crate::data::{self, Jvm};
use crate::findings::Finding;
use crate::groups::{Category, Nav};
use crate::procs::{AppInfo, ProcError, Process, Role, Runtime, SignStatus, Signing};

/// Readings of every process the sample lands, enough for the memory-growth rule.
const PROC_READINGS: usize = crate::proc_findings::GROWTH_READINGS + 4;
/// The sample's wall clock: 2026-11-20T12:00:00Z, hours after every sample process started.
pub const SAMPLE_NOW: i64 = 1_795_176_000;

fn sample_now() -> i64 {
    SAMPLE_NOW
}

fn sample_jvms(s: &mut App) {
    // `young` is the leaking JVM's young collections so far: (count, seconds), so its GC and
    // thread lanes have something to show without changing what the findings say.
    let jvm = |pid, main: &str, args: &str, ou: f64, fgc, fgct, young: (u64, f64), threads| Jvm {
        pid,
        main_class: main.into(),
        jdk_vendor: "Temurin".into(),
        jdk_version: "21.0.5".into(),
        vm_args: args.into(),
        thread_count: threads,
        gc: Some(data::Gc {
            ou,
            eu: 80_000.0,
            mu: 60_000.0,
            ygc: 40 + young.0,
            fgc,
            fgct,
            gct: fgct + 0.4 + young.1,
            ..data::Gc::default()
        }),
        classes: Some(data::Classes { loaded: 15_000 }),
    };
    const YOUNG_SECONDS: [f64; 8] = [0.0, 0.05, 0.17, 0.2, 0.4, 0.48, 0.63, 0.73];
    const THREADS: [u64; 8] = [46, 48, 48, 51, 49, 52, 55, 54];
    s.baseline.clear();
    for i in 0..8 {
        let step = i as f64;
        s.land(Ok(vec![
            jvm(
                4101,
                "com.acme.checkout.Server",
                "-Xmx2g -Xms512m -XX:+UseG1GC -Dspring.profiles.active=prod",
                900_000.0 + 110_000.0 * step,
                6,
                4.2,
                (3 * i as u64, YOUNG_SECONDS[i]),
                THREADS[i],
            ),
            jvm(
                4102,
                "org.gradle.launcher.daemon.bootstrap.GradleDaemon",
                "-Xmx4g -XX:+HeapDumpOnOutOfMemoryError",
                3_150_000.0,
                0,
                0.0,
                (0, 0.0),
                48,
            ),
            jvm(
                4103,
                "com.acme.search.Indexer",
                "-Xmx1g -XX:+HeapDumpOnOutOfMemoryError",
                210_000.0 + 15_000.0 * (step % 3.0),
                0,
                0.0,
                (0, 0.0),
                48,
            ),
        ]));
        if i == 2 {
            s.toggle_baseline();
        }
    }
}

/// One sample process: what it is, then how busy and how big.
struct P {
    pid: u32,
    ppid: u32,
    name: &'static str,
    role: Role,
    runtime: Runtime,
    app: Option<(&'static str, &'static str, &'static str)>,
    signing: Option<(SignStatus, &'static str)>,
    path: &'static str,
    args: &'static str,
    user: &'static str,
    cpu: f64,
    mb: f64,
    threads: u64,
}

const fn p(pid: u32, ppid: u32, name: &'static str, role: Role, runtime: Runtime) -> P {
    P {
        pid,
        ppid,
        name,
        role,
        runtime,
        app: None,
        signing: Some((SignStatus::Apple, "")),
        path: "",
        args: "",
        user: "jason",
        cpu: 0.0,
        mb: 4.0,
        threads: 3,
    }
}

impl P {
    const fn app(mut self, name: &'static str, path: &'static str, id: &'static str) -> P {
        self.app = Some((name, path, id));
        self
    }
    const fn signed(mut self, status: SignStatus, team: &'static str) -> P {
        self.signing = Some((status, team));
        self
    }
    const fn unread(mut self) -> P {
        self.signing = None;
        self
    }
    const fn at(mut self, path: &'static str, args: &'static str) -> P {
        self.path = path;
        self.args = args;
        self
    }
    const fn root(mut self) -> P {
        self.user = "root";
        self
    }
    const fn load(mut self, cpu: f64, mb: f64, threads: u64) -> P {
        self.cpu = cpu;
        self.mb = mb;
        self.threads = threads;
        self
    }
}

const SLACK: (&str, &str, &str) = ("Slack", "/Applications/Slack.app", "com.tinyspeck.slackmacgap");
const SAFARI: (&str, &str, &str) = ("Safari", "/Applications/Safari.app", "com.apple.Safari");
const CODE: (&str, &str, &str) = ("Visual Studio Code", "/Applications/Visual Studio Code.app", "com.microsoft.VSCode");
const NOTCHY: (&str, &str, &str) = ("Notchy", "/Users/jason/Applications/Notchy.app", "dev.jason.notchy");
const BURROW: (&str, &str, &str) = ("Burrow VPN", "/Applications/Burrow VPN.app", "net.burrow.vpn");
const ITERM: (&str, &str, &str) = ("iTerm", "/Applications/iTerm.app", "com.googlecode.iterm2");
const TERMINAL: (&str, &str, &str) = ("Terminal", "/System/Applications/Utilities/Terminal.app", "com.apple.Terminal");
const CLAUDE: (&str, &str, &str) = ("claude", "/Users/jason/.local/share/claude/claude.app", "com.anthropic.claude-code");
const OLLAMA: (&str, &str, &str) = ("Ollama", "/Applications/Ollama.app", "com.electron.ollama");

use Role::{AiAgent, App as AppRole, AppHelper, Cli, Shell as ShellRole, System, Terminal};
use Runtime::{Bun, Deno, Electron, Go, Jvm as JvmRt, Native, Node, Python, Ruby, Rust, Shell, Unknown};
use SignStatus::{Adhoc, DeveloperId, Invalid, Unsigned};

fn sample_procs() -> Vec<P> {
    vec![
        p(1, 0, "launchd", System, Native).at("/sbin/launchd", "").root().load(0.2, 22.0, 4),
        p(152, 1, "WindowServer", System, Native)
            .at("/System/Library/PrivateFrameworks/SkyLight.framework/Resources/WindowServer", "-daemon")
            .load(14.6, 412.0, 24),
        p(301, 1, "mds_stores", System, Native)
            .at("/System/Library/Frameworks/CoreServices.framework/Frameworks/Metadata.framework/Support/mds_stores", "")
            .root()
            .load(3.1, 118.0, 9),
        p(320, 1, "cfprefsd", System, Native).at("/usr/sbin/cfprefsd", "daemon").root().load(0.1, 7.0, 3),
        p(321, 1, "cfprefsd", System, Native).at("/usr/sbin/cfprefsd", "agent").load(0.0, 6.0, 3),
        p(340, 1, "trustd", System, Native).at("/usr/libexec/trustd", "").load(0.0, 11.0, 3),
        p(351, 1, "mdworker_shared", System, Native)
            .at("/System/Library/Frameworks/CoreServices.framework/Frameworks/Metadata.framework/Support/mdworker_shared", "-s mdworker")
            .load(0.6, 14.0, 4),
        p(352, 1, "mdworker_shared", System, Native)
            .at("/System/Library/Frameworks/CoreServices.framework/Frameworks/Metadata.framework/Support/mdworker_shared", "-s mdworker")
            .load(0.0, 12.0, 4),
        p(353, 1, "mdworker_shared", System, Native)
            .at("/System/Library/Frameworks/CoreServices.framework/Frameworks/Metadata.framework/Support/mdworker_shared", "-s mdworker")
            .load(0.0, 12.0, 4),
        // Apps and their helpers.
        p(900, 1, "Slack", AppRole, Electron)
            .app(SLACK.0, SLACK.1, SLACK.2)
            .signed(DeveloperId, "BQR82RBBHL")
            .at("/Applications/Slack.app/Contents/MacOS/Slack", "")
            .load(1.8, 236.0, 38),
        p(901, 900, "Slack Helper (Renderer)", AppHelper, Electron)
            .app(SLACK.0, SLACK.1, SLACK.2)
            .signed(DeveloperId, "BQR82RBBHL")
            .at(
                "/Applications/Slack.app/Contents/Frameworks/Slack Helper (Renderer).app/Contents/MacOS/Slack Helper (Renderer)",
                "--type=renderer --user-data-dir=/Users/jason/Library/Application Support/Slack --app-path=/Applications/Slack.app/Contents/Resources/app.asar --enable-sandbox --lang=en-US --num-raster-threads=4 --renderer-client-id=7 --launch-time-ticks=4410129883 --shared-files --field-trial-handle=1718379636,r,1520137412 --seatbelt-client=64",
            )
            .load(6.2, 380.0, 21),
        p(902, 900, "Slack Helper (GPU)", AppHelper, Electron)
            .app(SLACK.0, SLACK.1, SLACK.2)
            .signed(DeveloperId, "BQR82RBBHL")
            .at("/Applications/Slack.app/Contents/Frameworks/Slack Helper (GPU).app/Contents/MacOS/Slack Helper (GPU)", "--type=gpu-process")
            .load(2.4, 96.0, 14),
        p(903, 900, "Slack Helper", AppHelper, Electron)
            .app(SLACK.0, SLACK.1, SLACK.2)
            .signed(DeveloperId, "BQR82RBBHL")
            .at("/Applications/Slack.app/Contents/Frameworks/Slack Helper.app/Contents/MacOS/Slack Helper", "--type=utility --utility-sub-type=network.mojom.NetworkService")
            .load(0.3, 41.0, 9),
        p(910, 1, "Safari", AppRole, Native)
            .app(SAFARI.0, SAFARI.1, SAFARI.2)
            .at("/Applications/Safari.app/Contents/MacOS/Safari", "")
            .load(3.4, 288.0, 17),
        p(911, 1, "com.apple.WebKit.WebContent", AppHelper, Native)
            .app(SAFARI.0, SAFARI.1, SAFARI.2)
            .at("/System/Library/Frameworks/WebKit.framework/Versions/A/XPCServices/com.apple.WebKit.WebContent.xpc/Contents/MacOS/com.apple.WebKit.WebContent", "")
            .load(8.9, 512.0, 12),
        p(912, 1, "com.apple.WebKit.WebContent", AppHelper, Native)
            .app(SAFARI.0, SAFARI.1, SAFARI.2)
            .at("/System/Library/Frameworks/WebKit.framework/Versions/A/XPCServices/com.apple.WebKit.WebContent.xpc/Contents/MacOS/com.apple.WebKit.WebContent", "")
            .load(0.4, 164.0, 8),
        p(920, 1, "Code", AppRole, Electron)
            .app(CODE.0, CODE.1, CODE.2)
            .signed(DeveloperId, "UBF8T346G9")
            .at("/Applications/Visual Studio Code.app/Contents/MacOS/Electron", "")
            .load(2.2, 198.0, 34),
        p(921, 920, "Code Helper (Plugin)", AppHelper, Electron)
            .app(CODE.0, CODE.1, CODE.2)
            .signed(DeveloperId, "UBF8T346G9")
            .at("/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin)", "--type=utility --utility-sub-type=node.mojom.NodeService")
            .load(11.5, 622.0, 26),
        p(922, 920, "Code Helper (Renderer)", AppHelper, Electron)
            .app(CODE.0, CODE.1, CODE.2)
            .signed(DeveloperId, "UBF8T346G9")
            .at("/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Renderer).app/Contents/MacOS/Code Helper (Renderer)", "--type=renderer")
            .load(4.0, 344.0, 19),
        p(930, 1, "Notchy", AppRole, Native)
            .app(NOTCHY.0, NOTCHY.1, NOTCHY.2)
            .signed(Adhoc, "")
            .at("/Users/jason/Applications/Notchy.app/Contents/MacOS/Notchy", "")
            .load(0.2, 38.0, 5),
        p(940, 1, "Burrow VPN", AppRole, Native)
            .app(BURROW.0, BURROW.1, BURROW.2)
            .signed(DeveloperId, "7K3XQ2M9LP")
            .at("/Applications/Burrow VPN.app/Contents/MacOS/Burrow VPN", "")
            .load(0.4, 52.0, 6),
        p(941, 1, "BurrowHelper", AppHelper, Native)
            .app(BURROW.0, BURROW.1, BURROW.2)
            .signed(Invalid, "7K3XQ2M9LP")
            .at("/Library/PrivilegedHelperTools/net.burrow.vpn.helper", "")
            .root()
            .load(0.1, 9.0, 3),
        // Terminals, shells and what runs in them.
        p(1000, 1, "iTerm2", Terminal, Native)
            .app(ITERM.0, ITERM.1, ITERM.2)
            .signed(DeveloperId, "H7V7XYVQ7D")
            .at("/Applications/iTerm.app/Contents/MacOS/iTerm2", "")
            .load(1.1, 141.0, 11),
        p(1010, 1, "Terminal", Terminal, Native)
            .app(TERMINAL.0, TERMINAL.1, TERMINAL.2)
            .at("/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal", "")
            .load(0.3, 88.0, 7),
        p(1101, 1000, "zsh", ShellRole, Shell).at("/bin/zsh", "-zsh").load(0.0, 3.2, 1),
        p(1102, 1000, "zsh", ShellRole, Shell).at("/bin/zsh", "-zsh").load(0.0, 3.1, 1),
        p(1103, 1000, "zsh", ShellRole, Shell).at("/bin/zsh", "-zsh").load(0.1, 3.4, 1),
        p(1104, 1010, "zsh", ShellRole, Shell).at("/bin/zsh", "-zsh").load(0.0, 3.0, 1),
        p(1105, 1000, "zsh", ShellRole, Shell).at("/bin/zsh", "-zsh").load(0.0, 3.3, 1),
        p(1110, 1104, "bash", ShellRole, Shell).at("/bin/bash", "--login").load(0.0, 2.6, 1),
        p(1111, 1000, "fish", ShellRole, Shell)
            .signed(Adhoc, "")
            .at("/opt/homebrew/bin/fish", "-l")
            .load(0.0, 6.1, 1),
        // AI agents.
        p(1200, 1101, "claude", AiAgent, Bun)
            .app(CLAUDE.0, CLAUDE.1, CLAUDE.2)
            .signed(DeveloperId, "Q6L2SF6YDW")
            .at("/Users/jason/.local/share/claude/claude.app/Contents/MacOS/claude", "--resume")
            .load(9.4, 612.0, 22),
        p(1201, 1102, "claude", AiAgent, Bun)
            .app(CLAUDE.0, CLAUDE.1, CLAUDE.2)
            .signed(DeveloperId, "Q6L2SF6YDW")
            .at("/Users/jason/.local/share/claude/claude.app/Contents/MacOS/claude", "")
            .load(1.2, 288.0, 18),
        p(1210, 1103, "codex", AiAgent, Node)
            .signed(DeveloperId, "HX7739G8FX")
            .at("/opt/homebrew/bin/node", "/opt/homebrew/lib/node_modules/@openai/codex/bin/codex.js")
            .load(0.8, 154.0, 12),
        p(1220, 1105, "aider", AiAgent, Python)
            .signed(Adhoc, "")
            .at("/opt/homebrew/bin/python3.12", "/Users/jason/.local/bin/aider --model sonnet")
            .load(0.3, 212.0, 9),
        // Developer tools.
        p(4101, 1, "java", Cli, JvmRt)
            .signed(DeveloperId, "JCDTMS22B4")
            .at(
                "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home/bin/java",
                "-Xmx2g -Xms512m -XX:+UseG1GC -Dspring.profiles.active=prod -cp /srv/checkout/lib/* com.acme.checkout.Server",
            )
            .load(38.0, 1_840.0, 54),
        p(4102, 1, "java", Cli, JvmRt)
            .signed(DeveloperId, "JCDTMS22B4")
            .at(
                "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home/bin/java",
                "-Xmx4g -XX:+HeapDumpOnOutOfMemoryError -cp /Users/jason/.gradle/wrapper/dists/gradle-8.10/lib/gradle-launcher-8.10.jar org.gradle.launcher.daemon.bootstrap.GradleDaemon 8.10",
            )
            .load(4.5, 3_420.0, 48),
        p(4103, 1, "java", Cli, JvmRt)
            .signed(DeveloperId, "JCDTMS22B4")
            .at(
                "/Library/Java/JavaVirtualMachines/temurin-21.jdk/Contents/Home/bin/java",
                "-Xmx1g -XX:+HeapDumpOnOutOfMemoryError -jar /srv/search/indexer.jar",
            )
            .load(1.2, 420.0, 48),
        p(1300, 921, "rust-analyzer", Cli, Rust)
            .signed(Adhoc, "")
            .at("/Users/jason/.rustup/toolchains/stable-aarch64-apple-darwin/bin/rust-analyzer", "")
            .load(138.0, 1_120.0, 18),
        p(1310, 921, "gopls", Cli, Go)
            .signed(Adhoc, "")
            .at("/Users/jason/go/bin/gopls", "-mode=stdio")
            .load(0.9, 186.0, 14),
        p(1311, 1102, "gh", Cli, Go)
            .signed(DeveloperId, "4G9L8WJ34V")
            .at("/opt/homebrew/bin/gh", "pr status")
            .load(0.0, 24.0, 8),
        p(1320, 1101, "node", Cli, Node)
            .signed(DeveloperId, "HX7739G8FX")
            .at("/opt/homebrew/bin/node", "/Users/jason/site/node_modules/.bin/vite --port 5173")
            .load(2.6, 174.0, 11),
        p(1321, 1103, "bun", Cli, Bun)
            .signed(Adhoc, "")
            .at("/opt/homebrew/bin/bun", "run dev --hot")
            .load(1.4, 96.0, 9),
        p(1330, 1105, "deno", Cli, Deno)
            .signed(DeveloperId, "M33PMHMXH8")
            .at("/opt/homebrew/bin/deno", "run --allow-net server.ts")
            .load(0.2, 61.0, 10),
        p(1340, 1104, "python3", Cli, Python)
            .signed(Unsigned, "")
            .at("/Users/jason/.pyenv/versions/3.12.4/bin/python3", "-m http.server 8000")
            .load(0.0, 19.0, 2),
        p(1350, 1110, "ruby", Cli, Ruby)
            .at("/usr/bin/ruby", "/usr/local/bin/pod install")
            .load(12.0, 140.0, 4),
        p(1360, 1110, "deploy.sh", Cli, Shell).at("/bin/bash", "./scripts/deploy.sh staging").load(0.0, 2.4, 1),
        p(1370, 1, "ollama", AiAgent, Go)
            .app(OLLAMA.0, OLLAMA.1, OLLAMA.2)
            .signed(DeveloperId, "3MU9H2V9Y9")
            .at("/Applications/Ollama.app/Contents/Resources/ollama", "serve")
            .load(0.5, 66.0, 16),
        p(1380, 1101, "mytool", Cli, Unknown)
            .unread()
            .at("/Users/jason/bin/mytool", "--watch")
            .load(0.0, 5.0, 2),
        // A second JDK's `java`: same name, another program.
        p(4110, 1102, "java", Cli, JvmRt)
            .signed(DeveloperId, "TDTHCUPYFR")
            .at("/Library/Java/JavaVirtualMachines/zulu-17.jdk/Contents/Home/bin/java", "-jar /Users/jason/tools/bundletool.jar build-apks")
            .load(3.2, 310.0, 31),
        // What the real feed lists without a path or a signature: a zombie and processes caught
        // as they exited.
        p(7533, 1300, "<defunct>", Cli, Unknown).unread().load(0.0, 0.0, 0),
        p(7670, 1110, "(bash)", Cli, Unknown).unread().load(0.2, 2.3, 0),
    ]
}

/// Readable notes like the real feed's.
fn evidence(x: &P) -> Vec<String> {
    let role = match (x.role, x.app) {
        (AppRole | AppHelper | AiAgent | Terminal, Some((_, path, _))) => format!("role: inside bundle {path}"),
        (System, _) => "role: Apple platform path".to_owned(),
        (ShellRole, _) => format!("role: executable {} is a known shell", x.name),
        _ => "role: no app, system, shell, terminal or agent signal".to_owned(),
    };
    let runtime = match x.runtime {
        _ if x.path.is_empty() => "runtime: executable path not visible".to_owned(),
        Electron => "runtime: bundle links Electron Framework.framework".to_owned(),
        JvmRt => "runtime: interpreter executable java".to_owned(),
        Go => "runtime: Mach-O has a __go_buildinfo section".to_owned(),
        Rust => "runtime: Mach-O carries Rust panic symbols".to_owned(),
        Native => "runtime: Mach-O with no Go, Bun or Rust markers".to_owned(),
        Unknown => "runtime: nothing recognisable in the executable".to_owned(),
        r => format!("runtime: argv[0] names the {} interpreter", r.label()),
    };
    let mut out = vec![role, runtime];
    if let Some((status, team)) = x.signing {
        out.push(signing_evidence(status, team));
    }
    out
}

/// The `signing:` line as the real feed words it: only Apple's signatures are checked against
/// their anchor; the rest are what `codesign -dv` displays.
fn signing_evidence(status: SignStatus, team: &str) -> String {
    const VERDICT: &str = "verdict is for the file on disk";
    let id = match status {
        SignStatus::Apple => {
            return format!(r#"signing: platform binary validated by codesign --verify --ignore-resources -R="anchor apple"; {VERDICT}"#);
        }
        // The real feed rarely has these; worded like the displayed ones.
        Invalid => "invalid",
        Unsigned => "unsigned",
        Adhoc => "adhoc",
        DeveloperId => "developer_id",
        SignStatus::Unknown => "unknown",
    };
    if team.is_empty() {
        format!("signing: {id} as displayed by codesign -dv, not validated; {VERDICT}")
    } else {
        format!("signing: {id}, team {team} as displayed by codesign -dv, not validated; {VERDICT}")
    }
}

fn process(x: &P, reading: usize) -> Process {
    let step = reading as f64;
    // A small, fixed wobble so lines aren't flat; the two the findings are about move on purpose.
    let wobble = [1.0, 1.08, 0.94, 1.03, 0.97, 1.05, 0.92, 1.0, 1.06, 0.96, 1.02, 0.99][reading % 12];
    let cpu = match x.pid {
        1300 => 132.0 + 6.0 * (step % 3.0),
        _ => x.cpu * wobble,
    };
    let mb = match x.pid {
        901 => x.mb + 5.0 * step,
        _ => x.mb * (1.0 + (wobble - 1.0) * 0.1),
    };
    // Without a path the real feed's command line is the bare name, as `ps` shows it.
    let cmdline = match (x.path, x.args) {
        ("", _) => x.name.to_owned(),
        (path, "") => path.to_owned(),
        (path, args) => format!("{path} {args}"),
    };
    let mut p = Process {
        pid: x.pid,
        ppid: x.ppid,
        uid: if x.user == "root" { 0 } else { 501 },
        user: x.user.into(),
        command: x.name.into(),
        executable_path: x.path.into(),
        full_command_line: cmdline,
        rss_kib: mb * 1024.0,
        vsize_kib: mb * 1024.0 * 40.0,
        thread_count: x.threads,
        cpu_pct: (cpu * 10.0).round() / 10.0,
        mem_percent: mb / 32_768.0 * 100.0,
        started_at: format!("2026-11-19T{:02}:{:02}:04-05:00", 8 + x.pid % 9, x.pid % 60),
        role: x.role,
        runtime: x.runtime,
        app: x.app.map(|(name, path, id)| AppInfo { name: name.into(), bundle_path: path.into(), bundle_id: id.into() }),
        signing: x.signing.map(|(status, team)| Signing {
            status,
            team_id: team.into(),
            hardened_runtime: status == DeveloperId,
            validated: None,
        }),
        evidence: evidence(x),
        elapsed: None,
    };
    p.settle();
    p
}

/// Lands `PROC_READINGS` readings of the sample processes.
pub fn land_sample_procs(s: &mut App) {
    let all = sample_procs();
    s.cores = 10;
    s.wall = sample_now;
    for i in 0..PROC_READINGS {
        s.land_procs(Ok(all.iter().map(|x| process(x, i)).collect()));
    }
}

/// The overview, with the worst finding open.
pub fn sample_scene(s: &mut App) {
    sample_jvms(s);
    land_sample_procs(s);
    s.expanded = s.findings.first().map(Finding::id);
}

/// The leaking JVM's page.
pub fn detail_scene(s: &mut App) {
    sample_scene(s);
    s.select(Some(4101));
    s.expanded = None;
}

/// A claude agent's page: a Developer ID signature, as displayed and not verified.
pub fn signed_scene(s: &mut App) {
    sample_scene(s);
    s.show(Nav::Category(Category::Agents));
    s.open(1200);
    s.expanded = None;
}

/// Every process, from a reading 14 s ago; the one since failed.
pub fn stale_scene(s: &mut App) {
    all_scene(s);
    s.proc_updated = Some(s.now - 14.0);
    s.land_procs(Err(ProcError::Failed("spectra process failed: no processes found".into())));
}

/// Every process.
pub fn all_scene(s: &mut App) {
    sample_scene(s);
    s.show(Nav::All);
}

/// One category: apps.
pub fn apps_scene(s: &mut App) {
    sample_scene(s);
    s.show(Nav::Category(Category::Apps));
}

/// Every process, with Slack opened to its helpers.
pub fn group_scene(s: &mut App) {
    all_scene(s);
    s.toggle_group("app::Applications:Slack.app");
}

/// A Slack helper's page: its memory is climbing.
pub fn process_scene(s: &mut App) {
    sample_scene(s);
    s.show(Nav::Category(Category::Apps));
    s.open(901);
    s.expanded = None;
}

/// The JVMs only, as with a Spectra too old to list processes.
pub fn old_scene(s: &mut App) {
    sample_jvms(s);
    s.land_procs(Err(ProcError::TooOld("spectra can't classify processes (it has no --classify).".into())));
    s.expanded = s.findings.first().map(Finding::id);
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::app::init;
    use crate::procs::Runtime as Rt;

    #[test]
    fn the_sample_covers_every_role_and_signing_status_and_most_runtimes() {
        let mut s = init();
        sample_scene(&mut s);
        for role in [AppRole, AppHelper, AiAgent, Terminal, ShellRole, Cli, System] {
            assert!(s.procs.iter().any(|p| p.role == role), "{role:?}");
        }
        for st in [SignStatus::Apple, DeveloperId, Adhoc, Unsigned, Invalid, SignStatus::Unknown] {
            assert!(s.procs.iter().any(|p| p.sign_status() == st), "{st:?}");
        }
        let runtimes = Rt::ALL.iter().filter(|r| s.procs.iter().any(|p| p.runtime == **r)).count();
        assert_eq!(runtimes, Rt::ALL.len(), "every runtime");
        let rules: Vec<&str> = s.findings.iter().map(|f| f.rule).collect();
        for rule in ["proc-busy-cpu", "proc-memory-growth", "proc-invalid-signature", "proc-unidentified", "jvm-heap-pressure"] {
            assert!(rules.contains(&rule), "{rule} in {rules:?}");
        }
        // Like the real feed: Apple's checked, the rest only as displayed, and some rows with
        // neither a path nor a signature.
        for p in &s.procs {
            match p.sign_status() {
                SignStatus::Apple => assert!(p.validated(), "{}", p.name()),
                SignStatus::Unknown => assert!(p.signing_evidence().is_none()),
                _ => assert!(!p.validated() && p.signing_evidence().unwrap().contains("not validated"), "{}", p.name()),
            }
        }
        assert!(s.procs.iter().any(|p| p.executable_path.is_empty() && p.signing.is_none()));
        let ollama = s.process(1370).unwrap();
        assert_eq!((ollama.role, ollama.bundle_path()), (AiAgent, Some("/Applications/Ollama.app")));
    }
}
