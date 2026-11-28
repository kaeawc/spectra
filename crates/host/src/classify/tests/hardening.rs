//! Go's `hardening_test.go` (and `codesignout`'s `TestOnlyDetritus`).

use std::sync::Arc;

use spectra_base::fs::{Fs, OsFs};
use spectra_base::hostos::Kind;
use spectra_base::proc::{CancelToken, FakeRunner, Output, match_exact, match_prefix};

use super::basics::{ADHOC_CODESIGN, APPLE_CODESIGN, DEV_ID_CODESIGN, MAS_CODESIGN, UNSIGNED_CODESIGN, third_party_verify_fake};
use super::fixtures::*;
use crate::classify::bundle::MAX_PLIST_BYTES;
use crate::classify::engine::{CACHE_VERSION, ExeEntry};
use crate::classify::files::{NOT_REGULAR, TOO_LARGE, open_regular, read_file};
use crate::classify::role::{RoleInput, decide_role, interpreter_script};
use crate::classify::runtime::{BinaryVerdict, SymbolFacts, cxx_symbol, rust_verdict};
use crate::classify::signing::{DETRITUS_COMPLAINT, SigningVerdict, only_detritus};
use crate::classify::{Classifier, Input, Options, Role, Runtime, Signing, SigningStatus};

// An ad-hoc signature whose CodeDirectory claims a platform identifier: codesign -dv displays
// it, codesign --verify rejects it.
const SPOOF_PLATFORM_CODESIGN: &str = "Executable=/opt/evil
CodeDirectory v=20400 size=325 flags=0x2(adhoc) hashes=5+2 location=embedded
Platform identifier=15
Signature=adhoc
TeamIdentifier=not set
";
const SPOOF_LEAF_CODESIGN: &str = "Executable=/opt/evil
CodeDirectory v=20400 size=325 flags=0x0(none) hashes=5+2 location=embedded
Authority=Software Signing
Authority=Apple Code Signing Certification Authority
Authority=Apple Root CA
TeamIdentifier=not set
";

fn verify_fake(display: &str, verify_exit: i32) -> Arc<FakeRunner> {
    let f = FakeRunner::new();
    f.on(match_prefix("codesign", &["-dv"]), stderr(display, 0));
    f.on(match_exact("codesign", &["--verify", "--ignore-resources", "-R=anchor apple", "/opt/evil"]), stderr("", verify_exit));
    Arc::new(f)
}

fn darwin(uid: i64, command: &str, exe: &str) -> Input {
    Input { uid, command: command.into(), executable_path: exe.into(), command_line: exe.into(), ..Input::default() }
}

// Go: TestAppleClaimNeedsVerification.
#[test]
fn apple_claim_needs_verification() {
    let fs = FakeFs::new();
    fs.add("/opt/evil", build_macho(CPU_ARM, &[]));
    let one = Input { pid: 9, ..darwin(0, "evil", "/opt/evil") };
    let cases = [
        ("forged platform id", SPOOF_PLATFORM_CODESIGN, 3, SigningStatus::Invalid, Role::Cli, "claims platform binary but"),
        ("forged Software Signing leaf", SPOOF_LEAF_CODESIGN, 3, SigningStatus::Invalid, Role::Cli, "claims Software Signing leaf but"),
        ("genuine platform binary", APPLE_CODESIGN, 0, SigningStatus::Apple, Role::System, "platform binary validated by"),
    ];
    for (name, display, exit, status, role, ev) in cases {
        let r = classify_one(&Classifier::new(opts(Kind::Darwin, fs.clone(), verify_fake(display, exit))), one.clone());
        let s = r.signing.clone().expect("signing");
        assert!(s.status == status && r.role == role && s.validated == (status == SigningStatus::Apple), "{name}: {s:?} {:?}", r.role);
        let all = evidence(&r);
        assert!(all.contains(ev) && all.contains("file on disk"), "{name}: {all}");
    }
}

// Go: TestVerifyRunsOnlyForAppleClaimsAndAppleIssuedCerts.
#[test]
fn verify_runs_only_for_apple_claims_and_apple_issued_certs() {
    let fs = FakeFs::new();
    fs.add("/opt/x", build_macho(CPU_ARM, &[]));
    for display in [ADHOC_CODESIGN, UNSIGNED_CODESIGN] {
        let fake = codesign_fake(display);
        let r = classify_one(&Classifier::new(opts(Kind::Darwin, fs.clone(), fake.clone())), darwin(501, "x", "/opt/x"));
        assert!(calls(&fake) == 1 && r.signing.as_ref().is_some_and(|s| !s.validated), "{:?}", fake.calls());
        assert!(evidence(&r).contains("as displayed by codesign -dv, not validated; verdict is for the file on disk"), "{:?}", r.evidence);
    }
}

/// (name, display, --strict verify answer, status, validated, persisted, evidence)
type ThirdPartyCase<'a> = (&'a str, &'a str, Result<Output, &'a str>, SigningStatus, bool, bool, &'a str);

// Go: TestThirdPartySignatureVerification.
#[test]
fn third_party_signature_verification() {
    let fs = FakeFs::new();
    fs.add("/opt/x", build_macho(CPU_ARM, &[]));
    let fail = |s: &str| Ok(stderr(s, 1));
    let cases: [ThirdPartyCase<'_>; 8] = [
        (
            "intact Developer ID",
            DEV_ID_CODESIGN,
            Ok(Output::default()),
            SigningStatus::DeveloperId,
            true,
            true,
            "signing: developer_id, team ABCDE12345 validated by codesign --verify --strict; verdict is for the file on disk",
        ),
        (
            "intact Mac App Store",
            MAS_CODESIGN,
            Ok(Output::default()),
            SigningStatus::DeveloperId,
            true,
            true,
            "team BQR82RBBHL validated by",
        ),
        (
            "tampered Developer ID",
            DEV_ID_CODESIGN,
            fail("/opt/x: invalid signature (code or signature have been modified)\nIn architecture: arm64\n"),
            SigningStatus::Invalid,
            false,
            true,
            "displays an Apple-issued certificate but codesign --verify --strict failed",
        ),
        (
            "only --strict detritus, plain verify fails",
            DEV_ID_CODESIGN,
            fail("/opt/x: resource fork, Finder information, or similar detritus not allowed\n"),
            SigningStatus::DeveloperId,
            false,
            true,
            "rejected extended attributes or resource forks",
        ),
        (
            "executable vanished",
            DEV_ID_CODESIGN,
            fail("/opt/x: No such file or directory\n"),
            SigningStatus::DeveloperId,
            false,
            false,
            "could not run",
        ),
        ("verify killed by a signal", DEV_ID_CODESIGN, Ok(stderr("", -1)), SigningStatus::DeveloperId, false, false, "could not run"),
        (
            "verify usage error",
            DEV_ID_CODESIGN,
            Ok(stderr("codesign: internal error\n", 2)),
            SigningStatus::DeveloperId,
            false,
            false,
            "could not run",
        ),
        ("verify timed out", DEV_ID_CODESIGN, Err("context deadline exceeded"), SigningStatus::DeveloperId, false, false, "could not run"),
    ];
    for (name, display, verify, status, validated, persisted, ev) in cases {
        let fake = third_party_verify_fake(display, verify, None);
        let store = MemStore::new();
        let r = classify_one(
            &Classifier::new(with_store(opts(Kind::Darwin, fs.clone(), fake.clone()), store.clone())),
            darwin(501, "x", "/opt/x"),
        );
        let s = r.signing.clone().expect("signing");
        assert!(s.status == status && s.validated == validated, "{name}: {s:?} {:?}", fake.calls());
        assert_eq!(store.puts() > 0, persisted, "{name}");
        assert!(evidence(&r).contains(ev), "{name}: {:?}", r.evidence);
    }
}

// Go: TestVerifyRunnerFailureIsUnknownAndNotPersisted.
#[test]
fn verify_runner_failure_is_unknown_and_not_persisted() {
    let fs = FakeFs::new();
    fs.add("/opt/evil", build_macho(CPU_ARM, &[]));
    let fake = FakeRunner::new();
    fake.on(match_prefix("codesign", &["-dv"]), stderr(APPLE_CODESIGN, 0));
    fake.on_error(match_prefix("codesign", &["--verify"]), "context deadline exceeded");
    let store = MemStore::new();
    let r =
        classify_one(&Classifier::new(with_store(opts(Kind::Darwin, fs, Arc::new(fake)), store.clone())), darwin(0, "evil", "/opt/evil"));
    assert_eq!(r.signing.as_ref().map(|s| s.status), Some(SigningStatus::Unknown));
    assert!(r.role != Role::System && store.puts() == 0, "{:?}", r.role);
}

/// A fresh, empty directory under the system temp dir.
fn temp_dir(name: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!("spectra-classify-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("temp dir");
    dir
}

// Go: TestFIFOsAndOversizeFilesDoNotBlock.
#[test]
#[allow(clippy::disallowed_methods)]
fn fifos_and_oversize_files_do_not_block() {
    let dir = temp_dir("fifo");
    let app = dir.join("Foo.app/Contents");
    std::fs::create_dir_all(app.join("MacOS")).expect("mkdir");
    let mkfifo = |p: &std::path::Path| std::process::Command::new("mkfifo").arg(p).status().is_ok_and(|s| s.success());
    if !mkfifo(&app.join("Info.plist")) {
        let _ = std::fs::remove_dir_all(&dir);
        return; // Go skips when mkfifo is unavailable.
    }
    let fifo_exe = app.join("MacOS/Foo");
    assert!(mkfifo(&fifo_exe));
    let big = dir.join("Big.app/Contents");
    std::fs::create_dir_all(big.join("MacOS")).expect("mkdir");
    std::fs::File::create(big.join("Info.plist")).expect("create").set_len(4 << 30).expect("truncate");
    let big_exe = big.join("MacOS/Big");
    spectra_base::fsutil::write_file_atomic(&big_exe, &build_macho(CPU_ARM, &[]), 0o755).expect("write");

    let fake = codesign_fake(ADHOC_CODESIGN);
    let c = Classifier::new(Options { fs: Arc::new(OsFs), ..opts(Kind::Darwin, Arc::new(OsFs), fake.clone()) });
    let (fifo_s, big_s) = (fifo_exe.to_string_lossy().into_owned(), big_exe.to_string_lossy().into_owned());
    let (res, err) = c.classify(
        &[Input { pid: 1, ..darwin(501, "Foo", &fifo_s) }, Input { pid: 2, ..darwin(501, "Big", &big_s) }],
        &no_argv,
        &CancelToken::new(),
    );
    assert!(err.is_none());
    assert!(res[0].runtime == Runtime::Unknown && evidence(&res[0]).contains("not a regular file (p---------)"), "{:?}", res[0]);
    let big_app = res[1].app.clone().expect("big app");
    assert!(res[1].runtime == Runtime::Native && big_app.bundle_id.is_empty(), "{:?}", res[1]);
    assert!(fake.calls().iter().all(|c| c.args.last() != Some(&fifo_s)), "codesign was run on a FIFO");

    let fs: &dyn Fs = &OsFs;
    assert!(open_regular(fs, &fifo_s).err().is_some_and(|e| e.ends_with(NOT_REGULAR)));
    let plist = app.join("Info.plist").to_string_lossy().into_owned();
    assert!(read_file(fs, &plist, MAX_PLIST_BYTES).err().is_some_and(|e| e.ends_with(NOT_REGULAR)));
    let big_plist = big.join("Info.plist").to_string_lossy().into_owned();
    assert!(read_file(fs, &big_plist, MAX_PLIST_BYTES).err().is_some_and(|e| e.contains(TOO_LARGE)));
    std::fs::remove_dir_all(dir).expect("cleanup");
}

// Go: TestBundleReadsStopOnCancel.
#[test]
fn bundle_reads_stop_on_cancel() {
    let cancel = CancelToken::new();
    let fs = Arc::new(FakeFs { cancel_on_plist: Some(cancel.clone()), ..FakeFs::default() });
    let mut inputs = Vec::new();
    for i in 0..20 {
        let exe = format!("/Applications/A{i}.app/Contents/MacOS/A{i}");
        fs.add(&format!("/Applications/A{i}.app/Contents/Info.plist"), info_plist("a", "A"));
        fs.add(&exe, build_macho(CPU_ARM, &[]));
        inputs.push(Input { pid: i + 1, uid: 501, executable_path: exe, ..Input::default() });
    }
    let c = Classifier::new(Options { workers: 1, ..opts(Kind::Darwin, fs.clone(), codesign_fake(ADHOC_CODESIGN)) });
    let (_, err) = c.classify(&inputs, &no_argv, &cancel);
    let reads = fs.plist_reads.load(std::sync::atomic::Ordering::SeqCst);
    assert!(err.is_some() && reads == 1, "err={err:?} after {reads} bundle reads");
}

// Go: TestCacheMissesOnSameSizeSameMtimeReplacement.
#[test]
fn cache_misses_on_same_size_same_mtime_replacement() {
    let fs = FakeFs::new();
    let exe = "/Users/a/bin/tool";
    let rust = build_macho(CPU_ARM, &[Sect("__TEXT", "__const", &rust_data())]);
    let go_bin = build_macho(CPU_ARM, &[Sect("__DATA", "__go_buildinfo", &vec![0u8; rust_data().len()])]);
    assert_eq!(rust.len(), go_bin.len(), "fixtures differ in size");
    let file = |data: &[u8], ino, ctime| FakeFile { data: data.to_vec(), mtime: FIXTURE_TIME, ino, ctime };
    fs.set(exe, file(&go_bin, 1, 100_000_000_000));
    let store = MemStore::new();
    let run = || {
        classify_one(
            &Classifier::new(with_store(opts(Kind::Darwin, fs.clone(), codesign_fake(ADHOC_CODESIGN)), store.clone())),
            darwin(501, "tool", exe),
        )
        .runtime
    };
    assert_eq!(run(), Runtime::Go, "cold");
    // cp -p over the file: same size and mtime, new inode and ctime.
    fs.set(exe, file(&rust, 2, 200_000_000_000));
    assert_eq!(run(), Runtime::Rust, "same-size same-mtime replacement must miss");
    // Rewritten in place (same inode), mtime restored: only ctime moves.
    fs.set(exe, file(&go_bin, 2, 300_000_000_000));
    assert_eq!(run(), Runtime::Go, "in-place rewrite must miss");
}

// Go: TestCacheVersionPinsEntryShape. Go hashes the entry's reflected type; Rust pins the JSON
// an entry with every field set encodes to, which is what the store holds. If ExeEntry changes,
// bump CACHE_VERSION and update both pins.
#[test]
fn cache_version_pins_entry_shape() {
    let e = ExeEntry {
        binary: Some(BinaryVerdict { runtime: Runtime::Go, evidence: "e".into(), go_module: "m".into() }),
        signing: Some(SigningVerdict {
            signing: Signing { status: SigningStatus::Apple, team_id: "T".into(), hardened_runtime: true, validated: true },
            platform: true,
            claim: "c".into(),
            detritus: true,
            finder_metadata: true,
        }),
        retry_bin: true,
        retry_sign: true,
    };
    let json = String::from_utf8(spectra_base::gojson::to_vec(&e).expect("json")).expect("utf8");
    assert_eq!(CACHE_VERSION, "classify/v6");
    assert_eq!(
        json,
        r#"{"binary":{"runtime":"go","evidence":"e","go_module":"m"},"signing":{"signing":{"status":"apple","team_id":"T","hardened_runtime":true,"validated":true},"platform":true,"claim":"c","detritus":true,"finder_metadata":true}}"#
    );
    let back: ExeEntry = serde_json::from_str(&json).expect("decode");
    assert_eq!(back, ExeEntry { retry_bin: false, retry_sign: false, ..e });
    assert_eq!(serde_json::from_str::<ExeEntry>("{}").expect("empty"), ExeEntry::default());
}

// Go: TestAgentNeedsMoreThanAName.
#[test]
fn agent_needs_more_than_a_name() {
    let fs = FakeFs::new();
    let bin = build_macho(CPU_ARM, &[Sect("__TEXT", "__cstring", b"c\0")]);
    for p in [
        "/Users/a/bin/x",
        "/opt/homebrew/bin/amp",
        "/opt/homebrew/bin/goose",
        "/opt/homebrew/bin/crush",
        "/opt/homebrew/bin/gemini",
        "/Users/a/.local/bin/goose",
        "/Users/a/.amp/bin/amp",
        "/opt/homebrew/bin/node",
        "/Applications/Goose.app/Contents/MacOS/goose",
    ] {
        fs.add(p, bin.clone());
    }
    fs.add("/Applications/Goose.app/Contents/Info.plist", info_plist("com.block.goose", "Goose"));
    let target = "/opt/homebrew/lib/node_modules/@google/gemini-cli/dist/index.js";
    fs.link("/opt/homebrew/bin/gemini", target);
    fs.add(target, "js");
    let c = Classifier::new(opts(Kind::Darwin, fs, codesign_fake(ADHOC_CODESIGN)));
    let mk = |command: &str, exe: &str, cmdline: &str| Input {
        uid: 501,
        command: command.into(),
        executable_path: exe.into(),
        command_line: if cmdline.is_empty() { exe.into() } else { cmdline.into() },
        ..Input::default()
    };
    let cases = [
        ("exec -a claude", mk("claude", "/Users/a/bin/x", "claude"), Role::Cli),
        ("bare amp", mk("amp", "/opt/homebrew/bin/amp", ""), Role::Cli),
        ("bare goose (pressly migrations)", mk("goose", "/opt/homebrew/bin/goose", ""), Role::Cli),
        ("bare crush", mk("crush", "/opt/homebrew/bin/crush", ""), Role::Cli),
        ("bare gemini binary", mk("gemini", "/opt/homebrew/bin/gemini", ""), Role::Cli),
        ("goose at pressly install path", mk("goose", "/Users/a/.local/bin/goose", ""), Role::Cli),
        ("amp at installer path", mk("amp", "/Users/a/.amp/bin/amp", ""), Role::AiAgent),
        ("goose by bundle id", mk("goose", "/Applications/Goose.app/Contents/MacOS/goose", ""), Role::AiAgent),
        ("gemini script through npm bin link", mk("node", "/opt/homebrew/bin/node", "node /opt/homebrew/bin/gemini"), Role::AiAgent),
        ("argv0 only, exe hidden", mk("claude", "", "claude"), Role::AiAgent),
        ("ambiguous argv0 only", mk("goose", "", "goose"), Role::Cli),
    ];
    for (name, input, want) in cases {
        let hidden = input.executable_path.is_empty();
        let r = classify_one(&c, input);
        assert_eq!(r.role, want, "{name}: {:?}", r.evidence);
        if hidden && r.role == Role::AiAgent {
            assert!(r.evidence[0].contains("unverified"), "{:?}", r.evidence);
        }
    }
    let crush = RoleInput {
        os: Kind::Darwin,
        exe: "/opt/homebrew/bin/crush".into(),
        go_module: "github.com/charmbracelet/crush".into(),
        ..RoleInput::default()
    };
    let (role, ev) = decide_role(&crush);
    assert!(role == Role::AiAgent && ev.contains("Go module"), "{role:?} {ev}");
}

// Go: TestInterpreterScriptSkipsFlagValues.
#[test]
fn interpreter_script_skips_flag_values() {
    let cases = [
        ("node --require x app.js", Runtime::Node, "app.js"),
        ("node -r ./hook.js --import tsx /opt/x/cli.js serve", Runtime::Node, "/opt/x/cli.js"),
        ("node --max-old-space-size=4096 server.js", Runtime::Node, "server.js"),
        ("node -e console.log(1)", Runtime::Node, ""),
        ("node -- app.js", Runtime::Node, "app.js"),
        ("bun --cwd /srv run dev", Runtime::Bun, "dev"),
        ("bun -r ./preload.ts index.ts", Runtime::Bun, "index.ts"),
        ("deno run -c deno.json -A main.ts", Runtime::Deno, "main.ts"),
        ("deno run --inspect /tmp/claude.js", Runtime::Deno, "/tmp/claude.js"),
        ("deno run --inspect-brk /tmp/claude.js", Runtime::Deno, "/tmp/claude.js"),
        ("deno run --inspect-wait=127.0.0.1:9229 /tmp/claude.js", Runtime::Deno, "/tmp/claude.js"),
        ("deno run --config deno.json /tmp/claude.js", Runtime::Deno, "/tmp/claude.js"),
        ("node --inspect /tmp/claude.js", Runtime::Node, "/tmp/claude.js"),
        ("bun --inspect /tmp/claude.js", Runtime::Bun, "/tmp/claude.js"),
        ("python3 -W ignore -X dev -m aider", Runtime::Python, "aider"),
        ("python3 -X utf8 tool.py", Runtime::Python, "tool.py"),
        ("python3 -c print(1)", Runtime::Python, ""),
    ];
    for (cmdline, rt, want) in cases {
        assert_eq!(interpreter_script(cmdline, rt), want, "{cmdline}");
    }
}

/// (name, command line, known argv, lazily loaded argv, want)
type ArgvCase = (&'static str, String, Vec<String>, Option<Vec<String>>, Role);

// Go: TestInterpreterScriptKeepsArgvBoundaries.
#[test]
fn interpreter_script_keeps_argv_boundaries() {
    let c = Classifier::new(opts(Kind::Darwin, FakeFs::new(), codesign_fake(ADHOC_CODESIGN)));
    let node = "/opt/homebrew/bin/node";
    let gemini = "/home/Jane Doe/node_modules/@google/gemini-cli/dist/index.js";
    let argv = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
    let cases: [ArgvCase; 6] = [
        ("script path with a space", format!("node {gemini}"), argv(&["node", gemini]), None, Role::AiAgent),
        (
            "script path with a space, argv loaded lazily",
            format!("node {gemini}"),
            Vec::new(),
            Some(argv(&["node", gemini])),
            Role::AiAgent,
        ),
        // Without argv the joined string splits at the space: /home/Jane.
        ("script path with a space, no argv", format!("node {gemini}"), Vec::new(), None, Role::Cli),
        ("argv[0] with a space", "node codex server.js".into(), argv(&["node codex", "server.js"]), None, Role::Cli),
        ("argv[0] with a space, no argv", "node codex server.js".into(), Vec::new(), None, Role::AiAgent),
        ("argv unavailable falls back", "node /opt/homebrew/bin/codex".into(), Vec::new(), Some(Vec::new()), Role::AiAgent),
    ];
    for (name, cmdline, known, lazy, want) in cases {
        let input = Input {
            uid: 501,
            command: "node".into(),
            executable_path: node.into(),
            command_line: cmdline,
            argv: known,
            load_argv: lazy.is_some(),
            ..Input::default()
        };
        let loaded = lazy.clone().unwrap_or_default();
        let (res, _) = c.classify(&[input], &|_| loaded.clone(), &CancelToken::new());
        assert_eq!(res[0].role, want, "{name}: {:?}", res[0].evidence);
        assert_eq!(res[0].loaded_argv, lazy, "{name}: the loaded argv is kept");
    }
    let called = std::sync::atomic::AtomicBool::new(false);
    let native = Input {
        uid: 501,
        command: "tool".into(),
        executable_path: "/opt/tool/bin/tool".into(),
        command_line: "tool".into(),
        load_argv: true,
        ..Input::default()
    };
    let (res, _) = c.classify(
        &[native],
        &|_| {
            called.store(true, std::sync::atomic::Ordering::SeqCst);
            Vec::new()
        },
        &CancelToken::new(),
    );
    assert!(!called.load(std::sync::atomic::Ordering::SeqCst), "argv loaded for a process that is not an interpreter");
    assert_eq!(res[0].loaded_argv, None);
}

// Go: TestRustVerdict.
#[test]
fn rust_verdict_table() {
    let facts = |rust_entry, locals, cxx| SymbolFacts { rust_entry, locals, cxx, skipped: false };
    let libs = |v: &[&str]| v.iter().map(|s| s.to_string()).collect::<Vec<_>>();
    let cases = [
        ("rust main symbol", 5, facts(true, true, false), libs(&[]), Runtime::Rust, "lang_start"),
        ("C++ main with Rust linked, unstripped", 5, facts(false, true, true), libs(&[]), Runtime::Native, "links Rust std"),
        (
            "unstripped, links libc++, no lang_start",
            5,
            facts(false, true, false),
            libs(&["/usr/lib/libc++.1.dylib"]),
            Runtime::Native,
            "links Rust std",
        ),
        (
            "unstripped LTO Rust, no lang_start",
            5,
            facts(false, true, false),
            libs(&["/usr/lib/libSystem.B.dylib"]),
            Runtime::Rust,
            "link-time optimization",
        ),
        ("stripped C++ (adb)", 5, facts(false, false, true), libs(&[]), Runtime::Native, "links Rust std"),
        (
            "stripped, links libc++ (netsimd)",
            5,
            facts(false, false, false),
            libs(&["/usr/lib/libc++.1.dylib"]),
            Runtime::Native,
            "links Rust std",
        ),
        (
            "stripped ObjC main linking Rust",
            5,
            facts(false, false, false),
            libs(&["/usr/lib/libobjc.A.dylib"]),
            Runtime::Native,
            "Swift, Objective-C or Foundation",
        ),
        (
            "unstripped Swift main linking Rust",
            5,
            facts(false, true, false),
            libs(&["/usr/lib/swift/libswiftCore.dylib"]),
            Runtime::Native,
            "Swift, Objective-C or Foundation",
        ),
        (
            "Foundation C main linking Rust",
            5,
            facts(false, true, false),
            libs(&["/System/Library/Frameworks/Foundation.framework/Versions/C/Foundation"]),
            Runtime::Native,
            "Swift, Objective-C or Foundation",
        ),
        (
            "CoreFoundation stripped",
            5,
            facts(false, false, false),
            libs(&["/System/Library/Frameworks/CoreFoundation.framework/Versions/A/CoreFoundation"]),
            Runtime::Native,
            "Swift, Objective-C or Foundation",
        ),
        (
            "Rust main with lang_start still wins over ObjC libs",
            5,
            facts(true, false, false),
            libs(&["/usr/lib/libobjc.A.dylib"]),
            Runtime::Rust,
            "lang_start",
        ),
        ("stripped Rust (fd)", 5, facts(false, false, false), libs(&["/usr/lib/libSystem.B.dylib"]), Runtime::Rust, "stripped"),
        ("no markers", 0, facts(false, false, true), libs(&[]), Runtime::Native, "no Go, Bun or Rust"),
    ];
    for (name, hits, syms, libs, want, ev) in cases {
        let v = rust_verdict("Mach-O", hits, syms, &libs);
        assert!(v.runtime == want && v.evidence.contains(ev), "{name}: {v:?}");
    }
    for name in [
        "__RNCINvNtCsaeTlahLr9f4_3std2rt10lang_startNtNtB6_7process8ExitCodeE0Csl86PP5zXZpu_2rg", // v0
        "__ZN3std2rt10lang_start17h0123456789abcdefE",                                            // legacy
    ] {
        let mut s = SymbolFacts::default();
        s.add(name);
        assert!(s.rust_entry, "{name} not recognised as std::rt::lang_start");
    }
    let mut s = SymbolFacts::default();
    s.add("__RNCNCNCNvNtCsaeTlahLr9f4_3std2rt19lang_start_internal00s_0B9_");
    s.add("___cxa_thread_atexit_impl"); // Rust std on glibc imports this
    assert!(!s.rust_entry && !s.cxx, "{s:?}");
    for name in ["__ZNSt3__112basic_stringIcEE", "___gxx_personality_v0", "__Znwm", "___cxa_throw"] {
        assert!(cxx_symbol(name), "{name} not recognised as C++ runtime");
    }
}

// Go: TestAppleVerifyWithoutVerdictIsNotInvalidOrCached.
#[test]
fn apple_verify_without_verdict_is_not_invalid_or_cached() {
    let fs = FakeFs::new();
    fs.add("/opt/evil", build_macho(CPU_ARM, &[]));
    for exit in [-1, 2] {
        let store = MemStore::new();
        let c = Classifier::new(with_store(opts(Kind::Darwin, fs.clone(), verify_fake(SPOOF_PLATFORM_CODESIGN, exit)), store.clone()));
        let r = classify_one(&c, darwin(0, "evil", "/opt/evil"));
        assert!(
            r.signing.as_ref().is_some_and(|s| s.status != SigningStatus::Invalid) && store.puts() == 0,
            "exit {exit}: {:?}",
            r.signing
        );
    }
}

// Go: TestCacheMissesWhenBundleSealChanges.
#[test]
fn cache_misses_when_bundle_seal_changes() {
    let fs = FakeFs::new();
    let exe = "/Applications/Tool.app/Contents/MacOS/tool";
    let seal = "/Applications/Tool.app/Contents/_CodeSignature/CodeResources";
    fs.add(exe, build_macho(CPU_ARM, &[]));
    fs.add(seal, "seal-a");
    let store = MemStore::new();
    let run = || {
        let fake = codesign_fake(ADHOC_CODESIGN);
        classify_one(&Classifier::new(with_store(opts(Kind::Darwin, fs.clone(), fake.clone()), store.clone())), darwin(501, "tool", exe));
        calls(&fake)
    };
    assert!(run() > 0, "cold run did not call codesign");
    assert_eq!(run(), 0, "unchanged bundle re-ran codesign");
    fs.set(seal, FakeFile { data: b"seal-bb".to_vec(), mtime: FIXTURE_TIME + 3_600_000_000_000, ino: 0, ctime: 0 });
    assert!(run() > 0, "changed CodeResources did not invalidate the cached signing verdict");
}

// Go: TestOnlyDetritusWithNestedSubcomponents.
#[test]
fn only_detritus_with_nested_subcomponents() {
    let d = DETRITUS_COMPLAINT;
    let cases = [
        (
            "nested bundle detritus only",
            format!(
                "/opt/X.app: {d}\nIn subcomponent: /opt/X.app/Contents/Frameworks/Y.framework\n/opt/X.app/Contents/Frameworks/Y.framework/Y: {d}\nIn architecture: arm64\n"
            ),
            true,
        ),
        (
            "nested bundle also invalid",
            format!(
                "/opt/X.app: {d}\nIn subcomponent: /opt/X.app/Contents/Frameworks/Y.framework\n/opt/X.app/Contents/Frameworks/Y.framework: invalid signature (code or signature have been modified)\n"
            ),
            false,
        ),
        ("subcomponent header alone", "In subcomponent: /opt/X.app/Contents/Frameworks/Y.framework\n".to_string(), false),
    ];
    for (name, out, want) in cases {
        assert_eq!(only_detritus(&out), want, "{name}");
    }
}

// Go: codesignout's TestOnlyDetritus.
#[test]
fn only_detritus_table() {
    let d = DETRITUS_COMPLAINT;
    for (out, want) in [
        (format!("/a: {d}\n"), true),
        (format!("/a: {d}\nIn subcomponent: /a/b\n"), true),
        (format!("/a: {d}\n/a: a sealed resource is missing\n"), false),
        ("/a: invalid signature (code or signature have been modified)\n".to_string(), false),
        (format!("/a: {d}\nIn subcomponent: /a/b\n/a/b: invalid signature\n"), false),
        ("In subcomponent: /a/b\n".to_string(), false),
        (String::new(), false),
    ] {
        assert_eq!(only_detritus(&out), want, "{out:?}");
    }
}
