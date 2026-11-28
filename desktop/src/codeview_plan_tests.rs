//! Frames to targets and targets to steps, on a made-up filesystem.

use super::*;
use crate::codeview_tools::tests::FakeProbe;

const APP: &str = "/Applications/Sketchpad.app/Contents/MacOS/Sketchpad";

fn native(t: &Target) -> &Native {
    match t {
        Target::Native(n) => n,
        other => panic!("not native: {other:?}"),
    }
}

fn crash(frame: &str, path: &str, p: &FakeProbe) -> Target {
    resolve(
        &Request::Crash {
            frame: frame.into(),
            process_path: path.into(),
        },
        p,
    )
}

fn all_tools() -> Tools {
    Tools::with(&[
        (Tool::Lldb, "/usr/bin/lldb"),
        (Tool::Objdump, "/x/llvm-objdump"),
        (Tool::Otool, "/usr/bin/otool"),
        (Tool::Ghidra, "/g/support/analyzeHeadless"),
        (Tool::R2, "/opt/homebrew/bin/r2"),
        (Tool::RetDec, "/r/retdec-decompiler"),
        (Tool::Javap, "/usr/bin/javap"),
        (Tool::Java, "/usr/bin/java"),
        (Tool::Cfr, "/opt/homebrew/bin/cfr"),
        (Tool::Jadx, "/opt/homebrew/bin/jadx"),
        (Tool::Procyon, "/opt/homebrew/bin/procyon"),
        (Tool::FernflowerJar, "/ide/java-decompiler.jar"),
        (Tool::Python, "/usr/bin/python3"),
        (Tool::Pycdc, "/opt/homebrew/bin/pycdc"),
        (Tool::JsBeautify, "/opt/homebrew/bin/js-beautify"),
    ])
}

fn tools_of(steps: &[Step]) -> Vec<Option<Tool>> {
    steps.iter().map(|s| s.tool).collect()
}

/// The Python program inside lldb's `script exec(bytes.fromhex('…').decode())`.
fn script_in(args: &[String]) -> String {
    let cmd = args.last().unwrap();
    let hex = cmd
        .strip_prefix("script exec(bytes.fromhex('")
        .and_then(|r| r.strip_suffix("').decode())"))
        .expect("lldb runs one hex-encoded script");
    let bytes: Vec<u8> = (0..hex.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&hex[i..i + 2], 16).unwrap())
        .collect();
    String::from_utf8(bytes).unwrap()
}

#[test]
fn a_shared_cache_frame_goes_to_lldb_through_the_process() {
    let p = FakeProbe::new(&[], &[APP]);
    let t = crash("AppKit`-[NSApplication run] + 476", APP, &p);
    let n = native(&t);
    assert_eq!(n.image, "AppKit");
    assert_eq!(n.names, vec!["-[NSApplication run]", "run"]);
    assert_eq!(n.offset, Some(476));
    assert_eq!(n.binary, None);
    assert_eq!(n.host.as_deref(), Some(Path::new(APP)));
    let steps = plan(&t, &all_tools()).unwrap();
    // No decompiler or objdump can read the cache: lldb only.
    assert_eq!(tools_of(&steps), vec![Some(Tool::Lldb)]);
    let script = script_in(&steps[0].args);
    assert!(script.contains(&format!(
        "CreateTarget({}, None, None, True, err)",
        py_str(APP)
    )));
    assert!(script.contains("img = 'AppKit'"));
    assert!(script.contains("names = ['-[NSApplication run]', 'run']"));
    assert!(script.contains("off = 476"));
    assert_eq!(&steps[0].args[..3], ["--batch", "--no-lldbinit", "-o"]);
}

#[test]
fn a_gone_process_borrows_a_stock_host_for_the_cache() {
    let p = FakeProbe::new(
        &[],
        &["/System/Applications/TextEdit.app/Contents/MacOS/TextEdit"],
    );
    let t = crash(
        "libdispatch.dylib`_dispatch_lane_serial_drain + 744",
        APP,
        &p,
    );
    assert_eq!(
        native(&t).host.as_deref(),
        Some(Path::new(
            "/System/Applications/TextEdit.app/Contents/MacOS/TextEdit"
        ))
    );
}

#[test]
fn an_on_disk_image_tries_decompilers_then_lldb_then_objdump() {
    let fw = "/Applications/Sketchpad.app/Contents/Frameworks/InkKit.framework/InkKit";
    let p = FakeProbe::new(&[], &[APP, fw]);
    let t = crash("InkKit`ink_stroke_commit + 88", APP, &p);
    let n = native(&t);
    assert_eq!(n.binary.as_deref(), Some(Path::new(fw)));
    assert_eq!(n.host, None);
    let steps = plan(&t, &all_tools()).unwrap();
    assert_eq!(
        tools_of(&steps),
        vec![
            Some(Tool::Ghidra),
            Some(Tool::R2),
            Some(Tool::R2),
            Some(Tool::RetDec),
            Some(Tool::Lldb),
            Some(Tool::Objdump),
        ]
    );
    let ghidra = &steps[0];
    assert!(ghidra.needs_temp());
    assert_eq!(ghidra.output, Output::File("out.c".into()));
    assert_eq!(ghidra.files[0].0, "CodeViewDecompile.java");
    assert!(ghidra.args.contains(&"sym:ink_stroke_commit".to_owned()));
    assert!(ghidra.args.contains(&"-deleteProject".to_owned()));
    assert_eq!(steps[1].args[6], "aa; s sym._ink_stroke_commit; af; pdg");
    assert_eq!(steps[2].args[6], "aa; s sym._ink_stroke_commit; af; pdd");
    assert_eq!(
        steps[3].args,
        [
            "--select-functions",
            "ink_stroke_commit",
            "--cleanup",
            "-o",
            "{tmp}/out.c",
            fw
        ]
    );
    // An image on disk loads alone.
    assert!(script_in(&steps[4].args).contains("None, None, False, err)"));
    assert_eq!(
        steps[5].args,
        [
            "--no-show-raw-insn",
            "-C",
            "--disassemble-symbols=_ink_stroke_commit,ink_stroke_commit",
            fw
        ]
    );
    // Without llvm-objdump, otool.
    let t2 = Tools::with(&[(Tool::Otool, "/usr/bin/otool")]);
    let s2 = plan(&t, &t2).unwrap();
    assert_eq!(s2[0].args, ["-tV", "-p", "_ink_stroke_commit", fw]);
    assert_eq!(s2[0].parse, Parse::Otool);
}

#[test]
fn symbols_with_awkward_characters_reach_lldb_intact_and_nothing_else() {
    let p = FakeProbe::new(&[], &[APP, "/opt/homebrew/lib/libquirk.dylib"]);
    for sym in [
        "Foo.bar(with space:)",
        "price$total",
        "tick`tock",
        "(anonymous namespace)::Run()",
        "it's \\ \"quoted\"",
        "naïve_café",
    ] {
        let t = crash(&format!("libquirk.dylib`{sym} + 4"), APP, &p);
        let steps = plan(&t, &all_tools()).unwrap();
        // Ghidra, r2, RetDec and objdump take only plain C names; this one goes to lldb alone.
        assert_eq!(tools_of(&steps), vec![Some(Tool::Lldb)], "{sym}");
        let script = script_in(&steps[0].args);
        // The name sits in a Python literal: no raw quote, backtick or `$` reaches lldb.
        assert!(script.contains(&py_str(&clean_symbol(sym))), "{sym}");
        assert!(!steps[0].args[3].contains(sym), "{sym}");
        assert!(
            steps[0].args[3]
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"(').: ".contains(&b))
        );
    }
    assert_eq!(py_str("it's"), "'it\\'s'");
    assert_eq!(py_str("a\\b"), "'a\\\\b'");
    assert_eq!(py_str("tick`$(x)"), "'tick`$(x)'");
    assert_eq!(py_str("é"), "'\\U000000e9'");
}

#[test]
fn an_offset_only_frame_needs_lldb() {
    let p = FakeProbe::new(&[], &[APP]);
    let t = crash("Sketchpad + 0x1a2c4", APP, &p);
    let n = native(&t);
    assert!(n.names.is_empty());
    assert_eq!(n.offset, Some(0x1a2c4));
    assert_eq!(n.binary.as_deref(), Some(Path::new(APP)));
    let steps = plan(&t, &all_tools()).unwrap();
    assert_eq!(tools_of(&steps), vec![Some(Tool::Lldb)]);
    let s = script_in(&steps[0].args);
    assert!(s.contains("names = []") && s.contains("off = 107204"));
    let no = plan(&t, &Tools::with(&[(Tool::Objdump, "/x/llvm-objdump")])).unwrap_err();
    assert!(no.missing_tool);
    assert!(no.reason.contains("needs lldb"), "{}", no.reason);
}

#[test]
fn no_view_reasons_say_why() {
    let p = FakeProbe::new(&[], &[APP]);
    let why = |t: &Target, tools: &Tools| plan(t, tools).unwrap_err().reason;
    assert!(why(&crash("???", APP, &p), &all_tools()).contains("which binary"));
    assert!(why(&crash("Sketchpad", APP, &p), &all_tools()).contains("no symbol or offset"));
    let cache = crash("AppKit`-[NSView display] + 12", APP, &p);
    assert!(why(&cache, &Tools::with(&[(Tool::Objdump, "/o")])).contains("shared cache"));
    assert!(why(&cache, &Tools::default()).contains("xcode-select --install"));
    let sampled = |sym: &str, exe: &str| {
        resolve(
            &Request::Sampled {
                symbol: sym.into(),
                exe: exe.into(),
            },
            &p,
        )
    };
    assert!(why(&sampled("???", APP), &all_tools()).contains("no name"));
    assert!(why(&sampled("0x1a2c4", APP), &all_tools()).contains("no name"));
    assert!(why(&sampled("main", "/gone/app"), &all_tools()).contains("executable"));
}

#[test]
fn a_sampled_function_is_looked_up_across_the_process() {
    let exe = "/Users/dev/.cargo/bin/rust-analyzer";
    let p = FakeProbe::new(&[], &[exe]);
    let t = resolve(
        &Request::Sampled {
            symbol: "hir_ty::infer::InferenceContext::infer_body::h3b1f0c9a2d4e5f60".into(),
            exe: exe.into(),
        },
        &p,
    );
    let n = native(&t);
    assert_eq!(n.image, "");
    assert_eq!(
        n.names,
        vec!["hir_ty::infer::InferenceContext::infer_body", "infer_body"]
    );
    let steps = plan(&t, &all_tools()).unwrap();
    // A Rust path can't be spelled for objdump: lldb only, with what the process links.
    assert_eq!(tools_of(&steps), vec![Some(Tool::Lldb)]);
    assert!(script_in(&steps[0].args).contains("True, err)"));
    // A C name in the executable also gets objdump on the executable.
    let c = resolve(
        &Request::Sampled {
            symbol: "main".into(),
            exe: exe.into(),
        },
        &p,
    );
    assert_eq!(
        tools_of(&plan(&c, &all_tools()).unwrap()),
        vec![Some(Tool::Lldb), Some(Tool::Objdump)]
    );
}

#[test]
fn go_and_rust_names_are_cleaned() {
    assert_eq!(
        clean_symbol("core::ptr::drop_in_place<alloc::vec::Vec<u8>>::h0123456789abcdef"),
        "core::ptr::drop_in_place<alloc::vec::Vec<u8>>"
    );
    assert_eq!(
        clean_symbol("main.(*Server).handle"),
        "main.(*Server).handle"
    );
    // Not a hash: wrong length, or more letters after.
    assert_eq!(clean_symbol("a::h0123"), "a::h0123");
    assert_eq!(
        clean_symbol("a::h0123456789abcdefg"),
        "a::h0123456789abcdefg"
    );
}

#[test]
fn java_frames_split() {
    let f = parse_java("com.acme.checkout.inventory.StockLedger.reserve(StockLedger.java:212)")
        .unwrap();
    assert_eq!(
        (f.class.as_str(), f.method.as_str(), f.line),
        (
            "com.acme.checkout.inventory.StockLedger",
            "reserve",
            Some(212)
        )
    );
    let f = parse_java("at java.base@21.0.5/java.lang.Thread.run(Thread.java:1583)").unwrap();
    assert_eq!(f.class, "java.lang.Thread");
    assert_eq!(f.module.as_deref(), Some("java.base@21.0.5"));
    let f = parse_java("jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)").unwrap();
    assert!(f.native);
    let f = parse_java("app//com.acme.Job$Step.lambda$run$0(Job.java:40)").unwrap();
    assert_eq!(
        (f.class.as_str(), f.method.as_str()),
        ("com.acme.Job$Step", "lambda$run$0")
    );
    let f = parse_java("com.acme.Gen.make(Unknown Source)").unwrap();
    assert_eq!((f.file, f.line), (None, None));
    assert_eq!(parse_java("- locked <0x000000070f3a2b10>"), None);
    assert_eq!(parse_java(""), None);
}

#[test]
fn a_java_frame_finds_its_class_in_the_classpath() {
    let p = FakeProbe::new(
        &[],
        &[
            "/Library/Java/JavaVirtualMachines/jdk-21.jdk/Contents/Home/bin/java",
            "/Library/Java/JavaVirtualMachines/jdk-21.jdk/Contents/Home/bin/javap",
            "/srv/checkout/lib/core.jar",
            "/srv/checkout/lib/inventory.jar",
            "/srv/checkout/lib/inventory.jar!com/acme/checkout/inventory/StockLedger.class",
            "/srv/checkout/classes/com/acme/checkout/Main.class",
        ],
    );
    let cmd = "/Library/Java/JavaVirtualMachines/jdk-21.jdk/Contents/Home/bin/java -Xmx2g -cp /srv/checkout/classes:/srv/checkout/lib/*:relative/x.jar com.acme.checkout.Main --port 8080";
    let java = |frame: &str| {
        resolve(
            &Request::Java {
                frame: frame.into(),
                command_line: cmd.into(),
                exe: "/Library/Java/JavaVirtualMachines/jdk-21.jdk/Contents/Home/bin/java".into(),
            },
            &p,
        )
    };
    let Target::Jvm(j) =
        java("com.acme.checkout.inventory.StockLedger.reserve(StockLedger.java:212)")
    else {
        panic!("a Java target")
    };
    assert_eq!(
        j.classpath,
        [
            "/srv/checkout/classes",
            "/srv/checkout/lib/core.jar",
            "/srv/checkout/lib/inventory.jar"
        ]
        .map(PathBuf::from)
    );
    assert_eq!(
        j.at,
        Some(ClassAt::Jar("/srv/checkout/lib/inventory.jar".into()))
    );
    assert_eq!(
        j.javap.as_deref(),
        Some(Path::new(
            "/Library/Java/JavaVirtualMachines/jdk-21.jdk/Contents/Home/bin/javap"
        ))
    );
    let t = Target::Jvm(j);
    let steps = plan(&t, &all_tools()).unwrap();
    // A jar: CFR filters it to the class, jadx takes one class; Procyon and Fernflower would
    // decompile it all, so they sit out. javap comes from the JVM's own JDK.
    assert_eq!(
        tools_of(&steps),
        vec![Some(Tool::Cfr), Some(Tool::Jadx), Some(Tool::Javap)]
    );
    assert_eq!(
        steps[0].args,
        [
            "/srv/checkout/lib/inventory.jar",
            "--jarfilter",
            "^com\\.acme\\.checkout\\.inventory\\.StockLedger$"
        ]
    );
    assert_eq!(steps[1].output, Output::JavaIn("out".into()));
    assert_eq!(
        steps[2].program,
        Path::new("/Library/Java/JavaVirtualMachines/jdk-21.jdk/Contents/Home/bin/javap")
    );
    assert_eq!(
        steps[2].args,
        [
            "-c",
            "-p",
            "-l",
            "-cp",
            "/srv/checkout/classes:/srv/checkout/lib/core.jar:/srv/checkout/lib/inventory.jar",
            "com.acme.checkout.inventory.StockLedger"
        ]
    );
    // A class file in a folder: every decompiler can take it.
    let Target::Jvm(m) = java("com.acme.checkout.Main.main(Main.java:9)") else {
        panic!()
    };
    assert_eq!(
        m.at,
        Some(ClassAt::File(
            "/srv/checkout/classes/com/acme/checkout/Main.class".into()
        ))
    );
    let steps = plan(&Target::Jvm(m), &all_tools()).unwrap();
    assert_eq!(
        tools_of(&steps),
        vec![
            Some(Tool::Cfr),
            Some(Tool::Procyon),
            Some(Tool::FernflowerJar),
            Some(Tool::Jadx),
            Some(Tool::Javap)
        ]
    );
    assert_eq!(steps[2].program, Path::new("/usr/bin/java"));
    assert_eq!(
        steps[2].args[3],
        "/srv/checkout/classes/com/acme/checkout/Main.class"
    );
    assert_eq!(steps[2].args[4], "{tmp}/out");
    // The JDK's own classes: javap reads them from the runtime, no classpath.
    let Target::Jvm(th) = java("java.base@21.0.5/java.lang.Thread.run(Thread.java:1583)") else {
        panic!()
    };
    assert!(th.jdk && th.at.is_none());
    let steps = plan(&Target::Jvm(th), &all_tools()).unwrap();
    assert_eq!(steps[0].args, ["-c", "-p", "-l", "java.lang.Thread"]);
    // A native method has no bytecode.
    let why = plan(
        &java("jdk.internal.misc.Unsafe.park(java.base@21.0.5/Native Method)"),
        &all_tools(),
    )
    .unwrap_err();
    assert!(why.reason.contains("native method"));
    assert!(!why.missing_tool);
    // The JVM's own javap needs nothing found.
    assert_eq!(
        tools_of(&plan(&t, &Tools::default()).unwrap()),
        vec![Some(Tool::Javap)]
    );
    // Without that, a JDK on PATH or a decompiler, nothing.
    let Target::Jvm(mut bare) = t else { panic!() };
    bare.javap = None;
    let none = plan(&Target::Jvm(bare), &Tools::default()).unwrap_err();
    assert!(none.missing_tool && none.reason.contains("javap"));
}

/// The javap a JVM at `exe` gets, on a disk where `files` exist.
fn javap_for(exe: &str, p: &FakeProbe) -> Option<PathBuf> {
    let Target::Jvm(j) = resolve(
        &Request::Java {
            frame: "java.base/java.lang.Thread.run(Thread.java:1583)".into(),
            command_line: String::new(),
            exe: exe.into(),
        },
        p,
    ) else {
        panic!("a Java target")
    };
    j.javap
}

#[test]
fn a_jvms_own_javap_is_trusted_only_inside_a_jdk() {
    use crate::codeview_tools::tests::UID;
    let sys = "/Library/Java/JavaVirtualMachines/jdk-21.jdk/Contents/Home/bin";
    let sdk = "/Users/dev/.sdkman/candidates/java/21.0.5-tem/bin";
    let brew = "/opt/homebrew/Cellar/openjdk@21/21.0.5/libexec/openjdk.jdk/Contents/Home/bin";
    let dropped = "/tmp/drop/bin";
    let mut p = FakeProbe::new(
        &[],
        &[
            &format!("{sys}/javap"),
            &format!("{sdk}/javap"),
            &format!("{brew}/javap"),
            &format!("{dropped}/javap"),
            "/opt/homebrew/opt/openjdk@21/README",
        ],
    );
    p.links = vec![(
        PathBuf::from("/opt/homebrew/opt/openjdk@21"),
        PathBuf::from("/opt/homebrew/Cellar/openjdk@21/21.0.5"),
    )];
    let own = |bin: &str| Some(PathBuf::from(format!("{bin}/javap")));
    // The system's JDKs, the user's own (SDKMAN's), and Homebrew's through its link.
    assert_eq!(javap_for(&format!("{sys}/java"), &p), own(sys));
    p.modes = vec![
        (PathBuf::from(format!("{sdk}/javap")), (UID, 0o755)),
        (PathBuf::from(sdk), (UID, 0o755)),
    ];
    assert_eq!(javap_for(&format!("{sdk}/java"), &p), own(sdk));
    assert_eq!(javap_for(&format!("{brew}/java"), &p), own(brew));
    // A javap anywhere else is the process's choice, not a JDK's: detection's is used.
    assert_eq!(javap_for(&format!("{dropped}/java"), &p), None);
    let t = resolve(
        &Request::Java {
            frame: "java.base/java.lang.Thread.run(Thread.java:1583)".into(),
            command_line: String::new(),
            exe: format!("{dropped}/java"),
        },
        &p,
    );
    assert_eq!(
        plan(&t, &all_tools()).unwrap()[0].program,
        Path::new("/usr/bin/javap")
    );
    // Nor does `..` climb out of a JDK.
    assert_eq!(
        javap_for(
            "/Library/Java/JavaVirtualMachines/../../tmp/drop/bin/java",
            &p
        ),
        None
    );
    // java_home's answer counts as a JDK.
    p.java_home = Some(PathBuf::from("/tmp/drop"));
    assert_eq!(javap_for(&format!("{dropped}/java"), &p), own(dropped));
    // Another user's javap, or one (or its folder) others can write, isn't trusted.
    p.modes = vec![(PathBuf::from(format!("{sys}/javap")), (UID + 1, 0o755))];
    assert_eq!(javap_for(&format!("{sys}/java"), &p), None);
    p.modes = vec![(PathBuf::from(format!("{sys}/javap")), (0, 0o775))];
    assert_eq!(javap_for(&format!("{sys}/java"), &p), None);
    p.modes = vec![(PathBuf::from(sys), (UID, 0o757))];
    assert_eq!(javap_for(&format!("{sys}/java"), &p), None);
}

#[test]
fn jar_launched_jvms_and_odd_classpaths() {
    let p = FakeProbe::new(&[], &[]);
    assert_eq!(
        classpath("java -jar /srv/app/app.jar -cp /not/mine", &p),
        [PathBuf::from("/srv/app/app.jar")]
    );
    assert_eq!(
        classpath("java --class-path=/a:/b Main", &p),
        ["/a", "/b"].map(PathBuf::from)
    );
    assert!(classpath("java Main", &p).is_empty());
}

#[test]
fn script_frames_name_their_file_and_line() {
    let ps = |f: &str| parse_script(f);
    assert_eq!(
        ps("at handle (/srv/web/routes/cart.js:41:17)"),
        Some(("/srv/web/routes/cart.js".into(), Some(41)))
    );
    assert_eq!(
        ps("at /srv/web/server.mjs:9:3"),
        Some(("/srv/web/server.mjs".into(), Some(9)))
    );
    assert_eq!(
        ps("at load (file:///srv/web/lib/x.js:3:1)"),
        Some(("/srv/web/lib/x.js".into(), Some(3)))
    );
    assert_eq!(
        ps("File \"/srv/app/settings.py\", line 12, in load"),
        Some(("/srv/app/settings.py".into(), Some(12)))
    );
    assert_eq!(
        ps("/srv/app/jobs.py:7"),
        Some(("/srv/app/jobs.py".into(), Some(7)))
    );
}

#[test]
fn script_frames_read_source_or_compiled_python() {
    let p = FakeProbe::new(
        &[],
        &[
            "/srv/web/routes/cart.js",
            "/srv/app/__pycache__/settings.cpython-312.pyc",
        ],
    );
    let script = |f: &str| resolve(&Request::Script { frame: f.into() }, &p);
    let js = script("at handle (/srv/web/routes/cart.js:41:17)");
    let steps = plan(&js, &all_tools()).unwrap();
    // A formatter first, used only on a minified file, then the file itself.
    assert_eq!(tools_of(&steps), vec![Some(Tool::JsBeautify), None]);
    assert!(steps[0].only_if_minified);
    assert_eq!(
        steps[1].output,
        Output::Read("/srv/web/routes/cart.js".into())
    );
    let py = script("File \"/srv/app/settings.py\", line 12, in load");
    let Target::Script(sc) = &py else { panic!() };
    assert_eq!(
        sc.pyc.as_deref(),
        Some(Path::new("/srv/app/__pycache__/settings.cpython-312.pyc"))
    );
    let steps = plan(&py, &all_tools()).unwrap();
    assert_eq!(
        tools_of(&steps),
        vec![Some(Tool::Pycdc), Some(Tool::Python)]
    );
    assert_eq!(
        steps[1].args[..3],
        ["-I".to_owned(), "-c".into(), PY_DIS.into()]
    );
    let why = |f: &str| plan(&script(f), &all_tools()).unwrap_err().reason;
    assert!(why("at node:internal/modules/cjs/loader:1105:14").contains("built-in"));
    assert!(why("at /srv/web/gone.js:3:1").contains("isn't on this Mac"));
    assert!(why("at relative/x.js:3:1").contains("relative"));
    assert!(why("/srv/app/x.rb:3").contains(".rb"));
}
