//! Go's `classify_test.go`, `lto_test.go` and `finder_metadata_test.go`.

use std::sync::Arc;

use spectra_base::hostos::Kind;
use spectra_base::proc::{CancelToken, FakeRunner, Output, match_exact, match_prefix};

use super::fixtures::*;
use crate::classify::runtime::{count_markers, inspect_binary};
use crate::classify::signing::{DETRITUS_COMPLAINT, parse_codesign, read_signing};
use crate::classify::{Classifier, DEFAULT_MAX_SCAN_BYTES, Input, Role, Runtime, Signing, SigningStatus};

pub const APPLE_CODESIGN: &str = "Executable=/bin/ls
Identifier=com.apple.ls
Format=Mach-O universal (x86_64 arm64e)
CodeDirectory v=20400 size=325 flags=0x0(none) hashes=5+2 location=embedded
Platform identifier=26
Authority=macOS Software Signing
Authority=Apple Code Signing Certification Authority
Authority=Apple Root CA
TeamIdentifier=not set
";
pub const DEV_ID_CODESIGN: &str = "Executable=/Applications/Foo.app/Contents/MacOS/Foo
CodeDirectory v=20500 size=645 flags=0x10000(runtime) hashes=9+7 location=embedded
Authority=Developer ID Application: Foo Inc. (ABCDE12345)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
TeamIdentifier=ABCDE12345
";
pub const MAS_CODESIGN: &str = "CodeDirectory v=20500 size=645 flags=0x10000(runtime) hashes=9+7 location=embedded
Authority=Apple Mac OS Application Signing
Authority=Apple Worldwide Developer Relations Certification Authority
Authority=Apple Root CA
TeamIdentifier=BQR82RBBHL
";
pub const ADHOC_CODESIGN: &str = "Executable=/opt/homebrew/Cellar/ripgrep/15.2.0/bin/rg
CodeDirectory v=20400 size=50564 flags=0x2(adhoc) hashes=1574+2 location=embedded
Signature=adhoc
TeamIdentifier=not set
";
pub const UNSIGNED_CODESIGN: &str = "/tmp/a.out: code object is not signed at all\n";

fn signing(status: SigningStatus, team: &str, hardened: bool) -> Signing {
    Signing { status, team_id: team.into(), hardened_runtime: hardened, validated: false }
}

// Go: TestParseCodesign.
#[test]
fn parse_codesign_table() {
    let cases = [
        // An Apple claim is only a claim until codesign --verify settles it.
        ("apple", APPLE_CODESIGN, signing(SigningStatus::Unknown, "", false), "platform binary"),
        ("developer_id", DEV_ID_CODESIGN, signing(SigningStatus::DeveloperId, "ABCDE12345", true), ""),
        ("mac app store", MAS_CODESIGN, signing(SigningStatus::DeveloperId, "BQR82RBBHL", true), ""),
        ("adhoc", ADHOC_CODESIGN, signing(SigningStatus::Adhoc, "", false), ""),
        ("unsigned", UNSIGNED_CODESIGN, signing(SigningStatus::Unsigned, "", false), ""),
        ("garbage", "codesign: No such file or directory\n", signing(SigningStatus::Unknown, "", false), ""),
    ];
    for (name, out, want, claim) in cases {
        let got = parse_codesign(out);
        assert!(got.signing == want && got.claim == claim && !got.platform && !got.signing.validated, "{name}: {got:?}");
    }
}

// Go: TestBinaryRuntime.
#[test]
fn binary_runtime() {
    let go_macho = build_macho(CPU_ARM, &[Sect("__DATA", "__go_buildinfo", b"\xff Go buildinf:")]);
    let rust = rust_data();
    let rust_macho = build_macho(CPU_ARM, &[Sect("__TEXT", "__const", &rust)]);
    let plain = build_macho(CPU_ARM, &[Sect("__TEXT", "__cstring", b"hello\0")]);
    let bun_macho = build_macho(CPU_ARM, &[Sect("__BUN", "__bun", b"js")]);
    let fat = build_fat(&[
        (CPU_X86, build_macho(CPU_X86, &[Sect("__DATA", "__go_buildinfo", b"x")])),
        (CPU_ARM, build_macho(CPU_ARM, &[Sect("__DATA", "__go_buildinfo", b"x")])),
    ]);
    let cases: Vec<(&str, Vec<u8>, Runtime)> = vec![
        ("go macho", go_macho, Runtime::Go),
        ("rust macho", rust_macho, Runtime::Rust),
        ("plain macho", plain, Runtime::Native),
        ("bun macho", bun_macho, Runtime::Bun),
        ("fat go", fat, Runtime::Go),
        ("go elf", build_elf(&[(".go.buildinfo", b"\xff Go buildinf:")]), Runtime::Go),
        ("rust elf", build_elf(&[(".rodata", &rust)]), Runtime::Rust),
        ("plain elf", build_elf(&[(".rodata", b"hello")]), Runtime::Native),
        ("not a binary", b"#!/bin/sh\necho hi\n".to_vec(), Runtime::Unknown),
    ];
    for (name, data, want) in cases {
        // Only /proc/10/exe holds the code: the classifier must read the running image.
        let fs = FakeFs::new();
        fs.add("/proc/10/exe", data);
        let c = Classifier::new(opts(Kind::Linux, fs, Arc::new(FakeRunner::new())));
        let r = classify_one(&c, input(10, 501, "tool", "/opt/tool/bin/tool", "/opt/tool/bin/tool"));
        assert_eq!(r.runtime, want, "{name}: {:?}", r.evidence);
    }
}

// Go: TestFatPicksHostSliceAndSaysSo.
#[test]
fn fat_picks_host_slice_and_says_so() {
    let rust = rust_data();
    let fat = build_fat(&[
        (CPU_X86, build_macho(CPU_X86, &[Sect("__TEXT", "__const", &rust)])),
        (CPU_ARM, build_macho(CPU_ARM, &[Sect("__TEXT", "__const", &rust)])),
    ]);
    let fs = FakeFs::new();
    fs.add("/usr/local/bin/fd", fat);
    for (arch, cpu) in [("arm64", "CpuArm64"), ("amd64", "CpuAmd64")] {
        let (v, _) = inspect_binary(fs.as_ref(), "/usr/local/bin/fd", DEFAULT_MAX_SCAN_BYTES, arch);
        assert_eq!(v.runtime, Runtime::Rust, "{v:?}");
        assert!(v.evidence.ends_with(&format!(" (universal binary, {cpu} slice)")), "{v:?}");
    }
}

// Go: TestRustScanIsBounded.
#[test]
fn rust_scan_is_bounded() {
    let mut data = vec![0u8; 4096];
    data.extend(rust_data());
    let fs = FakeFs::new();
    fs.add("/bin/big", build_macho(CPU_ARM, &[Sect("__TEXT", "__const", &data)]));
    assert_eq!(inspect_binary(fs.as_ref(), "/bin/big", 1024, "arm64").0.runtime, Runtime::Native, "marker past the byte cap");
    assert_eq!(inspect_binary(fs.as_ref(), "/bin/big", 1 << 20, "arm64").0.runtime, Runtime::Rust, "marker inside the cap");
}

// Go: TestCountMarkersAcrossChunkBoundary.
#[test]
fn count_markers_across_chunk_boundary() {
    let mut data = vec![0u8; (256 << 10) - 3];
    data.extend_from_slice(b"/rustc/");
    data.extend(vec![0u8; 100]);
    let mut budget = 1i64 << 30;
    assert_eq!(count_markers(&data, &mut budget), 1);
}

// Go: TestUnreadableBinaryIsUnknown.
#[test]
fn unreadable_binary_is_unknown() {
    let fs = FakeFs::new();
    fs.add("/proc/5/exe", "x");
    fs.deny("/proc/5/exe", true);
    let c = Classifier::new(opts(Kind::Linux, fs, Arc::new(FakeRunner::new())));
    let (res, err) = c.classify(
        &[input(5, 0, "secret", "/usr/local/bin/secret", "secret"), input(6, 0, "gone", "/nope/gone", "gone")],
        &no_argv,
        &CancelToken::new(),
    );
    assert!(err.is_none());
    for r in &res {
        assert!(r.runtime == Runtime::Unknown && evidence(r).contains("unreadable"), "{r:?}");
    }
    assert_eq!(res[0].evidence[1], "runtime: executable unreadable: open /proc/5/exe: permission denied");
    assert_eq!(res[1].evidence[1], "runtime: executable unreadable: stat /proc/6/exe: no such file or directory");
}

// Go: TestInterpreterAndElectronRuntime.
#[test]
fn interpreter_and_electron_runtime() {
    let fs = FakeFs::new();
    fs.add("/Applications/Slack.app/Contents/Info.plist", info_plist("com.tinyspeck.slackmacgap", "Slack"));
    fs.add("/Applications/Slack.app/Contents/MacOS/Slack", "bin");
    fs.add("/Applications/Slack.app/Contents/Frameworks/Electron Framework.framework/Electron Framework", "fw");
    let helper = "/Applications/Slack.app/Contents/Frameworks/Slack Helper (Renderer).app/Contents/MacOS/Slack Helper (Renderer)";
    fs.add(helper, "bin");
    for p in [
        "/usr/bin/java",
        "/opt/homebrew/bin/node",
        "/opt/homebrew/bin/bun",
        "/opt/homebrew/bin/deno",
        "/usr/bin/python3",
        "/opt/homebrew/bin/python3.12",
        "/usr/bin/ruby",
        "/bin/zsh",
        "/opt/homebrew/bin/fish",
    ] {
        fs.add(p, "bin");
    }
    let c = Classifier::new(opts(Kind::Darwin, fs, codesign_fake(ADHOC_CODESIGN)));
    let helper_cmd = format!("{helper} --type=renderer");
    let cases = [
        ("/usr/bin/java", "/usr/bin/java -jar app.jar", Runtime::Jvm, Role::Cli),
        ("/opt/homebrew/bin/node", "node server.js", Runtime::Node, Role::Cli),
        ("/opt/homebrew/bin/bun", "bun run dev", Runtime::Bun, Role::Cli),
        ("/opt/homebrew/bin/deno", "deno run -A main.ts", Runtime::Deno, Role::Cli),
        ("/usr/bin/python3", "python3 x.py", Runtime::Python, Role::Cli),
        ("/opt/homebrew/bin/python3.12", "python3.12", Runtime::Python, Role::Cli),
        ("/usr/bin/ruby", "ruby x.rb", Runtime::Ruby, Role::Cli),
        ("/bin/zsh", "-zsh", Runtime::Shell, Role::Shell),
        ("/opt/homebrew/bin/fish", "fish", Runtime::Shell, Role::Shell),
        ("/Applications/Slack.app/Contents/MacOS/Slack", "/Applications/Slack.app/Contents/MacOS/Slack", Runtime::Electron, Role::App),
        (helper, helper_cmd.as_str(), Runtime::Electron, Role::AppHelper),
    ];
    for (exe, cmdline, want, role) in cases {
        let r = classify_one(&c, input(100, 501, exe.rsplit('/').next().unwrap_or(exe), exe, cmdline));
        assert!(r.runtime == want && r.role == role, "{exe}: {:?} {:?} {:?}", r.role, r.runtime, r.evidence);
    }
    let r = classify_one(&c, Input { pid: 7, executable_path: helper.into(), command_line: helper.into(), ..Input::default() });
    let app = r.app.expect("helper app");
    assert_eq!(
        (app.name.as_str(), app.bundle_id.as_str(), app.bundle_path.as_str()),
        ("Slack", "com.tinyspeck.slackmacgap", "/Applications/Slack.app")
    );
}

fn darwin(uid: i64, command: &str, exe: &str, cmdline: &str) -> Input {
    let cmdline = if cmdline.is_empty() { exe } else { cmdline };
    Input { uid, command: command.into(), executable_path: exe.into(), command_line: cmdline.into(), ..Input::default() }
}

// Go: TestRolePrecedence — every precedence rule documented in role.rs.
#[test]
fn role_precedence() {
    let fs = FakeFs::new();
    let bin = build_macho(CPU_ARM, &[Sect("__TEXT", "__cstring", b"c\0")]);
    fs.add("/Applications/Ghostty.app/Contents/Info.plist", info_plist("com.mitchellh.ghostty", "ghostty"));
    fs.add("/Applications/Warp.app/Contents/Info.plist", info_plist("dev.warp.Warp-Stable", "stable"));
    fs.add("/Applications/Ollama.app/Contents/Info.plist", info_plist("com.electron.ollama", "Ollama"));
    fs.add("/Applications/Xcode.app/Contents/Info.plist", info_plist("com.apple.dt.Xcode", "Xcode"));
    let py_fw =
        "/opt/homebrew/Cellar/python@3.12/3.12.1/Frameworks/Python.framework/Versions/3.12/Resources/Python.app/Contents/MacOS/Python";
    let claude_app = "/Users/a/Library/Application Support/Claude/claude-code/2.1.321/ddbdf2b334e1/claude.app/Contents/MacOS/claude";
    for p in [
        "/Users/a/.local/share/claude/versions/2.1.220",
        "/opt/homebrew/bin/node",
        "/opt/homebrew/bin/python3",
        "/opt/homebrew/bin/codex",
        "/Applications/Ghostty.app/Contents/MacOS/ghostty",
        "/Applications/Warp.app/Contents/MacOS/stable",
        "/opt/homebrew/bin/tmux",
        "/bin/zsh",
        "/Applications/Ollama.app/Contents/Resources/ollama",
        "/Applications/Ollama.app/Contents/MacOS/Ollama",
        "/System/Library/CoreServices/Dock.app/Contents/MacOS/Dock",
        "/usr/libexec/trustd",
        "/usr/sbin/cfprefsd",
        "/bin/ls",
        "/usr/bin/sudo",
        "/Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild",
        py_fw,
        "/Users/a/go/bin/gopls",
        "/Applications/Ghostty.app/Contents/MacOS/ghostty-helper",
        claude_app,
    ] {
        fs.add(p, bin.clone());
    }
    let runner = FakeRunner::new();
    runner.on(match_exact("codesign", &["-dv", "--verbose=2", "/bin/ls"]), stderr(APPLE_CODESIGN, 0));
    runner.on(match_exact("codesign", &["-dv", "--verbose=2", "/usr/bin/sudo"]), stderr(APPLE_CODESIGN, 0));
    runner.on(match_prefix("codesign", &[]), stderr(ADHOC_CODESIGN, 0));
    let c = Classifier::new(opts(Kind::Darwin, fs, Arc::new(runner)));
    let py_cmd = format!("{py_fw} x.py");
    let cases = [
        (
            "native claude by path shape",
            darwin(501, "claude", "/Users/a/.local/share/claude/versions/2.1.220", "claude --resume"),
            Role::AiAgent,
            Runtime::Native,
        ),
        (
            "agent npm package on node",
            darwin(501, "node", "/opt/homebrew/bin/node", "node /opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js"),
            Role::AiAgent,
            Runtime::Node,
        ),
        (
            "agent script on node",
            darwin(501, "node", "/opt/homebrew/bin/node", "node --no-warnings /opt/homebrew/bin/codex exec"),
            Role::AiAgent,
            Runtime::Node,
        ),
        (
            "agent python module",
            darwin(501, "python3", "/opt/homebrew/bin/python3", "python3 -m aider --model x"),
            Role::AiAgent,
            Runtime::Python,
        ),
        (
            "agent word in args is not an agent",
            darwin(501, "node", "/opt/homebrew/bin/node", "node server.js --name claude"),
            Role::Cli,
            Runtime::Node,
        ),
        ("agent by exe", darwin(501, "codex", "/opt/homebrew/bin/codex", "codex"), Role::AiAgent, Runtime::Native),
        (
            "agent inside app bundle",
            darwin(501, "ollama", "/Applications/Ollama.app/Contents/Resources/ollama", "ollama serve"),
            Role::AiAgent,
            Runtime::Native,
        ),
        ("agent shipped as its own app", darwin(501, "claude", claude_app, ""), Role::AiAgent, Runtime::Native),
        (
            "terminal app by exe",
            darwin(501, "ghostty", "/Applications/Ghostty.app/Contents/MacOS/ghostty", ""),
            Role::Terminal,
            Runtime::Native,
        ),
        (
            "terminal app by bundle name",
            darwin(501, "stable", "/Applications/Warp.app/Contents/MacOS/stable", ""),
            Role::Terminal,
            Runtime::Native,
        ),
        (
            "terminal helper stays helper",
            darwin(501, "ghostty-helper", "/Applications/Ghostty.app/Contents/MacOS/ghostty-helper", ""),
            Role::AppHelper,
            Runtime::Native,
        ),
        ("multiplexer", darwin(501, "tmux", "/opt/homebrew/bin/tmux", ""), Role::Terminal, Runtime::Native),
        ("shell", darwin(501, "-zsh", "/bin/zsh", ""), Role::Shell, Runtime::Shell),
        ("app main", darwin(501, "Ollama", "/Applications/Ollama.app/Contents/MacOS/Ollama", ""), Role::App, Runtime::Native),
        (
            "os bundle is system",
            darwin(501, "Dock", "/System/Library/CoreServices/Dock.app/Contents/MacOS/Dock", ""),
            Role::System,
            Runtime::Native,
        ),
        ("libexec daemon", darwin(501, "trustd", "/usr/libexec/trustd", ""), Role::System, Runtime::Native),
        ("sbin daemon", darwin(501, "cfprefsd", "/usr/sbin/cfprefsd", ""), Role::System, Runtime::Native),
        ("user platform tool is cli", darwin(501, "ls", "/bin/ls", ""), Role::Cli, Runtime::Native),
        ("root platform tool is system", darwin(0, "sudo", "/usr/bin/sudo", ""), Role::System, Runtime::Native),
        (
            "xcode toolchain is cli",
            darwin(501, "xcodebuild", "/Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild", ""),
            Role::Cli,
            Runtime::Native,
        ),
        ("framework python launcher is cli", darwin(501, "Python", py_fw, &py_cmd), Role::Cli, Runtime::Python),
        ("plain user binary", darwin(501, "gopls", "/Users/a/go/bin/gopls", ""), Role::Cli, Runtime::Native),
    ];
    for (name, input, role, runtime) in cases {
        let r = classify_one(&c, input);
        assert!(r.role == role && r.runtime == runtime, "{name}: {:?}/{:?} {:?}", r.role, r.runtime, r.evidence);
    }
    let xc = classify_one(&c, darwin(501, "xcodebuild", "/Applications/Xcode.app/Contents/Developer/usr/bin/xcodebuild", ""));
    assert_eq!(xc.app.expect("xcode app").bundle_id, "com.apple.dt.Xcode");
    let py = classify_one(&c, darwin(501, "Python", py_fw, ""));
    assert!(py.app.is_none(), "framework-internal Python.app reported as app {:?}", py.app);
}

// Go: TestLinuxRoles.
#[test]
fn linux_roles() {
    let fs = FakeFs::new();
    let elf_bin = build_elf(&[(".rodata", b"x")]);
    for pid in ["300", "302", "303", "305"] {
        fs.add(&format!("/proc/{pid}/exe"), elf_bin.clone());
    }
    // A real file at the argv[0] path: it must still not be trusted.
    fs.add("/usr/sbin/sshd", elf_bin);
    let fake = Arc::new(FakeRunner::new());
    let c = Classifier::new(opts(Kind::Linux, fs, fake.clone()));
    let mk = |pid, ppid, uid, command: &str, exe: &str, cmdline: &str| Input {
        pid,
        ppid,
        uid,
        command: command.into(),
        executable_path: exe.into(),
        command_line: cmdline.into(),
        ..Input::default()
    };
    let cases = [
        (mk(40, 2, 0, "kworker/0:1", "", "[kworker/0:1]"), Role::System, Runtime::Native),
        (
            mk(300, 1, 0, "systemd-journal", "/usr/lib/systemd/systemd-journald", "/usr/lib/systemd/systemd-journald"),
            Role::System,
            Runtime::Native,
        ),
        // exec -a /usr/sbin/sshd ./evil as root: no visible executable, so argv[0] decides nothing.
        (mk(301, 1, 0, "sshd", "", "/usr/sbin/sshd -D"), Role::Cli, Runtime::Unknown),
        (mk(302, 1, 1000, "sshd", "/usr/sbin/sshd", "sshd"), Role::Cli, Runtime::Native),
        (mk(303, 1, 1000, "htop", "/usr/bin/htop", "htop"), Role::Cli, Runtime::Native),
        // Interpreter guessed from the comm name only, and marked unverified.
        (mk(304, 1, 1000, "python3", "", "python3 x.py"), Role::Cli, Runtime::Python),
        // Root daemon whose argv[0] claims a system path; exe says otherwise.
        (mk(305, 1, 0, "sshd", "/tmp/evil", "/usr/sbin/sshd -D"), Role::Cli, Runtime::Native),
    ];
    for (input, role, runtime) in cases {
        let pid = input.pid;
        let r = classify_one(&c, input);
        assert!(r.role == role && r.runtime == runtime, "pid {pid}: {:?}/{:?} {:?}", r.role, r.runtime, r.evidence);
        assert!(r.signing.is_none() && r.app.is_none(), "pid {pid}: Linux result carries signing/app");
        if pid == 304 {
            assert!(evidence(&r).contains("from argv[0], unverified"), "{:?}", r.evidence);
        }
    }
    assert_eq!(calls(&fake), 0, "Linux classification ran commands");
    let (_, err) = read_signing(Kind::Linux, fake.as_ref(), &CancelToken::new(), "/usr/bin/htop");
    assert!(err.is_some() && calls(&fake) == 0, "readSigning on Linux must not run codesign");
}

// Go: TestSigningOncePerExecutable.
#[test]
fn signing_once_per_executable() {
    let fs = FakeFs::new();
    fs.add("/Applications/Foo.app/Contents/Info.plist", info_plist("com.foo", "Foo"));
    fs.add("/Applications/Foo.app/Contents/MacOS/Foo", build_macho(CPU_ARM, &[]));
    let fake = codesign_fake(DEV_ID_CODESIGN);
    let c = Classifier::new(opts(Kind::Darwin, fs, fake.clone()));
    let one = darwin(501, "Foo", "/Applications/Foo.app/Contents/MacOS/Foo", "");
    let (res, err) = c.classify(&[one.clone(), one.clone(), one], &no_argv, &CancelToken::new());
    assert!(err.is_none());
    // One display and one --verify --strict, however many processes share it.
    assert_eq!(calls(&fake), 2);
    let want = Signing { status: SigningStatus::DeveloperId, team_id: "ABCDE12345".into(), hardened_runtime: true, validated: true };
    assert_eq!(res[2].signing.as_ref(), Some(&want));
    assert_eq!(res[2].role, Role::App);
}

// Go: TestDiskCacheHitAndMiss.
#[test]
fn disk_cache_hit_and_miss() {
    let fs = FakeFs::new();
    let exe = "/Users/a/go/bin/tool";
    fs.add(exe, build_macho(CPU_ARM, &[Sect("__DATA", "__go_buildinfo", b"x")]));
    let store = MemStore::new();
    let one = darwin(501, "tool", exe, "");
    let run = |fake: &Arc<FakeRunner>| {
        classify_one(&Classifier::new(with_store(opts(Kind::Darwin, fs.clone(), fake.clone()), store.clone())), one.clone())
    };

    let cold = codesign_fake(ADHOC_CODESIGN);
    let r = run(&cold);
    assert!(r.runtime == Runtime::Go && calls(&cold) == 1 && store.puts() == 1, "cold: {r:?}");

    // A fresh Classifier (a second CLI run) is warm from disk: no codesign, not even opened.
    fs.deny(exe, true);
    let warm = codesign_fake(ADHOC_CODESIGN);
    let r = run(&warm);
    assert!(
        r.runtime == Runtime::Go && r.signing.as_ref().map(|s| s.status) == Some(SigningStatus::Adhoc) && calls(&warm) == 0,
        "warm: {r:?}"
    );
    fs.deny(exe, false);

    // Rebuilt binary, same size but newer mtime: must miss.
    let rust = rust_data();
    fs.set(
        exe,
        FakeFile {
            data: build_macho(CPU_ARM, &[Sect("__TEXT", "__const", &rust)]),
            mtime: FIXTURE_TIME + 60_000_000_000,
            ino: 0,
            ctime: 0,
        },
    );
    let miss = codesign_fake(ADHOC_CODESIGN);
    let r = run(&miss);
    assert!(r.runtime == Runtime::Rust && calls(&miss) == 1, "after mtime change: {r:?}");

    // Size change with the same mtime also misses.
    fs.set(
        exe,
        FakeFile {
            data: build_macho(CPU_ARM, &[Sect("__DATA", "__go_buildinfo", b"xy")]),
            mtime: FIXTURE_TIME + 60_000_000_000,
            ino: 0,
            ctime: 0,
        },
    );
    let miss = codesign_fake(ADHOC_CODESIGN);
    let r = run(&miss);
    assert!(r.runtime == Runtime::Go && calls(&miss) == 1, "after size change: {r:?}");
}

// Go: TestRunnerFailureIsNotPersisted.
#[test]
fn runner_failure_is_not_persisted() {
    let fs = FakeFs::new();
    fs.add("/opt/x", build_macho(CPU_ARM, &[]));
    let store = MemStore::new();
    let c = Classifier::new(with_store(opts(Kind::Darwin, fs, Arc::new(FakeRunner::new())), store.clone()));
    let r = classify_one(&c, darwin(501, "x", "/opt/x", ""));
    assert_eq!(r.signing.map(|s| s.status), Some(SigningStatus::Unknown));
    assert_eq!(store.puts(), 0);
}

// Go: TestCancelledContext.
#[test]
fn cancelled_context() {
    let fs = FakeFs::new();
    fs.add("/opt/x", build_macho(CPU_ARM, &[]));
    let cancel = CancelToken::new();
    cancel.cancel();
    let fake = codesign_fake(ADHOC_CODESIGN);
    let (res, err) = Classifier::new(opts(Kind::Darwin, fs, fake.clone())).classify(&[darwin(501, "x", "/opt/x", "")], &no_argv, &cancel);
    assert!(err.is_some() && res.len() == 1 && res[0].runtime == Runtime::Unknown && calls(&fake) == 0, "{res:?}");
    assert_eq!(err.map(|e| e.to_string()).as_deref(), Some("context canceled"));
    assert_eq!(res[0].evidence[1], "runtime: classification cancelled before the binary was read");
}

// Go: TestUnstrippedRustWithoutLangStart (lto_test.go): rustc -C lto=fat can inline
// std::rt::lang_start into main while other local symbols survive.
#[test]
fn unstripped_rust_without_lang_start() {
    let cases: [(&str, Vec<ElfSym<'_>>, Runtime, &str); 3] = [
        (
            "LTO Rust: locals, no lang_start, no C++ runtime",
            vec![
                ElfSym("_ZN4core3fmt5write17h0123456789abcdefE", true),
                ElfSym("_ZN5hello4main17hfedcba9876543210E", true),
                ElfSym("main", false),
            ],
            Runtime::Rust,
            "link-time optimization",
        ),
        (
            "plain Rust: lang_start defined",
            vec![ElfSym("_ZN3std2rt10lang_start17h0123456789abcdefE", true), ElfSym("main", false)],
            Runtime::Rust,
            "defines std::rt::lang_start",
        ),
        (
            "unstripped C++ main linking Rust (adb-like)",
            vec![
                ElfSym("_ZN4core3fmt5write17h0123456789abcdefE", true),
                ElfSym("_ZNSt3__112basic_stringIcNS_11char_traitsIcEENS_9allocatorIcEEED1Ev", false),
                ElfSym("__gxx_personality_v0", false),
                ElfSym("main", false),
            ],
            Runtime::Native,
            "with a C++ runtime",
        ),
    ];
    for (name, syms, want, ev) in cases {
        let fs = FakeFs::new();
        fs.add("/proc/10/exe", build_elf_with_symtab(&rust_data(), &syms));
        let c = Classifier::new(opts(Kind::Linux, fs, Arc::new(FakeRunner::new())));
        let r = classify_one(&c, input(10, 1000, "hello", "/opt/hello", "/opt/hello"));
        assert!(r.runtime == want && evidence(&r).contains(ev), "{name}: {:?} {:?}", r.runtime, r.evidence);
    }
}

/// Go's `thirdPartyVerifyFake`.
pub fn third_party_verify_fake(display: &str, verify: Result<Output, &str>, plain: Option<Result<Output, &str>>) -> Arc<FakeRunner> {
    let f = FakeRunner::new();
    f.on(match_prefix("codesign", &["-dv"]), stderr(display, 0));
    let add = |args: &[&str], r: Result<Output, &str>| match r {
        Ok(o) => {
            f.on(match_exact("codesign", args), o);
        }
        Err(e) => {
            f.on_error(match_exact("codesign", args), e);
        }
    };
    add(&["--verify", "--strict", "/opt/x"], verify);
    add(&["--verify", "/opt/x"], plain.unwrap_or(Ok(stderr("/opt/x: invalid signature\n", 1))));
    Arc::new(f)
}

// Go: TestFinderMetadataPlainVerify.
#[test]
fn finder_metadata_plain_verify() {
    let fs = FakeFs::new();
    fs.add("/opt/x", build_macho(CPU_ARM, &[]));
    let detritus = format!("/opt/x: {DETRITUS_COMPLAINT}\nIn subcomponent: /opt/x/F\n");
    let invalid = stderr("/opt/x: invalid signature\n", 1);
    let cases = [
        (
            "plain passes",
            stderr(&detritus, 1),
            Output::default(),
            true,
            1,
            true,
            "has Finder metadata (codesign --strict rejects it); code signature verified without --strict",
        ),
        ("plain fails", stderr(&detritus, 1), invalid.clone(), false, 1, true, "rejected extended attributes or resource forks"),
        ("plain crashed with -1", stderr(&detritus, 1), stderr("", -1), false, 1, false, "could not run"),
        ("plain usage error", stderr(&detritus, 1), stderr("", 2), false, 1, false, "could not run"),
        ("non-detritus failure is not retried", invalid, Output::default(), false, 0, true, "failed"),
    ];
    for (name, strict, plain, validated, plain_runs, persisted, ev) in cases {
        let fake = third_party_verify_fake(DEV_ID_CODESIGN, Ok(strict), Some(Ok(plain)));
        let store = MemStore::new();
        let c = Classifier::new(with_store(opts(Kind::Darwin, fs.clone(), fake.clone()), store.clone()));
        let r = classify_one(&c, darwin(501, "x", "/opt/x", ""));
        let s = r.signing.clone().expect("signing");
        assert_eq!(s.validated, validated, "{name}");
        if validated {
            assert_eq!(s.status, SigningStatus::DeveloperId, "{name}");
        }
        let runs = fake.calls().iter().filter(|c| c.name == "codesign" && c.args.join(" ") == "--verify /opt/x").count();
        assert_eq!((runs, store.puts() > 0), (plain_runs, persisted), "{name}");
        assert!(evidence(&r).contains(ev), "{name}: {:?}", r.evidence);
    }
}
