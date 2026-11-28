//! Reading each tool's output, from invented fixtures (tests/fixtures/codeview).

use super::*;
use crate::codeview_plan::{Native, Script};

const LLDB: &str = include_str!("../tests/fixtures/codeview/lldb-native.txt");
const JAVAP: &str = include_str!("../tests/fixtures/codeview/javap.txt");
const CFR: &str = include_str!("../tests/fixtures/codeview/cfr.txt");
const OBJDUMP: &str = include_str!("../tests/fixtures/codeview/objdump.txt");
const OTOOL: &str = include_str!("../tests/fixtures/codeview/otool.txt");
const PSEUDO: &str = include_str!("../tests/fixtures/codeview/pseudo-c.txt");
const PYDIS: &str = include_str!("../tests/fixtures/codeview/pydis.txt");
const R2_NO_PLUGIN: &str = include_str!("../tests/fixtures/codeview/r2-no-plugin.txt");

fn native(names: &[&str], offset: Option<u64>) -> Target {
    Target::Native(Native {
        image: "WidgetKitCore".into(),
        names: names.iter().map(|n| (*n).to_owned()).collect(),
        offset,
        binary: None,
        host: None,
    })
}

fn ledger(line: Option<u32>) -> Target {
    Target::Jvm(Jvm {
        class: "com.acme.checkout.inventory.StockLedger".into(),
        method: "reserve".into(),
        line,
        jdk: false,
        classpath: Vec::new(),
        at: None,
        javap: None,
    })
}

#[test]
fn lldb_marks_the_offsets_instruction() {
    let t = native(&["TimelineCache.entry(for:)"], Some(344));
    let p = parse(Parse::Lldb, LLDB, &t).unwrap();
    // The echo and the marker lines are gone; the function's header leads.
    assert_eq!(p.lines[0], "WidgetKitCore`TimelineCache.entry(for:):");
    assert!(
        !p.lines
            .iter()
            .any(|l| l.starts_with("(lldb)") || l.contains("CODEVIEW"))
    );
    let m = p.mark.expect("the offset is marked");
    assert_eq!(p.lines[m], "0x2a1c40f58 <+344>: brk    #0x1");
    assert_eq!(p.note, "The instruction at +344 is marked.");
    // Without lldb's address, `<+344>` still finds it.
    let no_at: String = LLDB
        .lines()
        .filter(|l| !l.starts_with("CODEVIEW_AT"))
        .collect::<Vec<_>>()
        .join("\n");
    assert_eq!(parse(Parse::Lldb, &no_at, &t).unwrap().mark, Some(m));
    // No offset, nothing marked.
    let p = parse(Parse::Lldb, &no_at, &native(&["x"], None)).unwrap();
    assert_eq!(p.mark, None);
    assert!(p.note.contains("no offset"));
    // An offset past the end.
    let p = parse(Parse::Lldb, &no_at, &native(&["x"], Some(9000))).unwrap();
    assert_eq!(p.mark, None);
    assert!(p.note.contains("Couldn't find +9000"));
}

#[test]
fn lldb_failures_are_reasons() {
    let t = native(&["TimelineCache.entry(for:)"], Some(344));
    let e = parse(Parse::Lldb, "(lldb) script exec(…)\nCODEVIEW_NONE\n", &t).unwrap_err();
    assert_eq!(
        e,
        "lldb found no function named TimelineCache.entry(for:) in WidgetKitCore"
    );
    let e = parse(Parse::Lldb, "CODEVIEW_ERR=unable to open file\n", &t).unwrap_err();
    assert!(e.contains("unable to open file"));
    let e = parse(
        Parse::Lldb,
        "Traceback (most recent call last):\n  File \"<string>\", line 3\nNameError: name 't' is not defined\n",
        &t,
    )
    .unwrap_err();
    assert!(e.contains("NameError"));
    assert!(parse(Parse::Lldb, "(lldb) script exec(…)\n\n", &t).is_err());
}

#[test]
fn objdump_and_otool_mark_from_the_functions_start() {
    let t = native(&["ledger_flush"], Some(0x18));
    let p = parse(Parse::Objdump, OBJDUMP, &t).unwrap();
    assert_eq!(p.lines[0], "0000000100003f10 <_ledger_flush>:");
    assert_eq!(
        p.lines[p.mark.unwrap()].trim_start(),
        "100003f28:     \tbl\t0x100004a20 <_$s6Ledger5EntryV4hashSivg>"
    );
    let p = parse(Parse::Otool, OTOOL, &t).unwrap();
    assert_eq!(p.lines[0], "_ledger_flush:");
    // The next label ends the function.
    assert!(!p.lines.iter().any(|l| l.contains("_ledger_open")));
    assert_eq!(
        p.lines[p.mark.unwrap()],
        "0000000100003f28\tldp\tx29, x30, [sp, #0x20]"
    );
    assert!(
        parse(
            Parse::Objdump,
            "\nDisassembly of section __TEXT,__text:\n",
            &t
        )
        .is_err()
    );
    assert!(
        parse(
            Parse::Otool,
            "/opt/tools/bin/ledgerd:\n(__TEXT,__text) section\n",
            &t
        )
        .is_err()
    );
}

#[test]
fn demanglers_replace_names_one_for_one() {
    let mut lines: Vec<String> = OBJDUMP.lines().map(str::to_owned).collect();
    let swift = mangled(&lines, true);
    assert_eq!(swift, ["_$s6Ledger5EntryV4hashSivg"]);
    let cpp = mangled(&lines, false);
    assert_eq!(cpp, ["__ZN6ledger5Store5flushEv"]);
    demangle(&mut lines, &swift, "Ledger.Entry.hash.getter : Swift.Int\n");
    demangle(&mut lines, &cpp, "ledger::Store::flush()\n");
    let text = lines.join("\n");
    assert!(text.contains("<Ledger.Entry.hash.getter : Swift.Int>"));
    assert!(text.contains("<ledger::Store::flush()>"));
    // A demangler that answers a different number of lines changes nothing.
    let mut again: Vec<String> = OBJDUMP.lines().map(str::to_owned).collect();
    demangle(&mut again, &swift, "one\ntwo\n");
    assert_eq!(again.join("\n"), OBJDUMP.trim_end_matches('\n'));
}

#[test]
fn pseudo_c_needs_code() {
    let p = parse(Parse::PseudoC, PSEUDO, &native(&["ledger_flush"], Some(8))).unwrap();
    assert_eq!(p.lines[0], "/* WARNING: Unknown calling convention */");
    assert_eq!(p.mark, None);
    // r2 without its decompiler plugin prints a complaint, not code: the next step runs.
    let e = parse(Parse::PseudoC, R2_NO_PLUGIN, &native(&["x"], None)).unwrap_err();
    assert!(e.contains("Cannot find function"));
    assert!(parse(Parse::PseudoC, "", &native(&["x"], None)).is_err());
}

#[test]
fn javap_keeps_the_method_and_marks_the_lines_bytecode() {
    let p = parse(Parse::Javap, JAVAP, &ledger(Some(212))).unwrap();
    assert_eq!(
        p.lines[1],
        "public class com.acme.checkout.inventory.StockLedger {"
    );
    // Only reserve's block: not the constructor or onHand.
    let text = p.lines.join("\n");
    assert!(text.contains("public synchronized boolean reserve(java.lang.String, int);"));
    assert!(!text.contains("public int onHand(java.lang.String);"));
    assert!(!text.contains("public com.acme.checkout.inventory.StockLedger();"));
    assert_eq!(p.lines.last().map(String::as_str), Some("}"));
    // `line 212: 28`: offset 28 is marked.
    let m = p.mark.unwrap();
    assert_eq!(p.lines[m].trim(), "28: aload_0");
    assert_eq!(p.note, "Line 212 starts at the bytecode marked (28).");
    // A line the table doesn't list.
    let p = parse(Parse::Javap, JAVAP, &ledger(Some(999))).unwrap();
    assert_eq!(p.mark, None);
    // A constructor is found by the class's name.
    let ctor = Target::Jvm(Jvm {
        method: "<init>".into(),
        line: Some(19),
        ..match ledger(None) {
            Target::Jvm(j) => j,
            _ => unreachable!(),
        }
    });
    let p = parse(Parse::Javap, JAVAP, &ctor).unwrap();
    assert_eq!(p.lines[p.mark.unwrap()].trim(), "4: aload_0");
    let e = parse(
        Parse::Javap,
        "Error: class not found: com.acme.checkout.inventory.StockLedger\n",
        &ledger(Some(212)),
    )
    .unwrap_err();
    assert!(e.contains("couldn't find com.acme.checkout.inventory.StockLedger"));
}

#[test]
fn decompiled_java_marks_the_method() {
    let p = parse(Parse::JavaDecompiled, CFR, &ledger(Some(212))).unwrap();
    let m = p.mark.unwrap();
    assert_eq!(
        p.lines[m].trim(),
        "public synchronized boolean reserve(String sku, int count) {"
    );
    assert!(p.note.contains("line 212 can't be placed"));
    assert!(
        parse(
            Parse::JavaDecompiled,
            "Error: could not load",
            &ledger(None)
        )
        .is_err()
    );
}

fn script(line: Option<u32>) -> Target {
    Target::Script(Script {
        lang: crate::codeview_plan::Lang::Js,
        path: Some("/srv/web/routes/cart.js".into()),
        pyc: None,
        line,
    })
}

#[test]
fn source_files_are_numbered_and_their_line_marked() {
    let src = "const a = 1;\nfunction go() {\n  throw new Error('x');\n}\n";
    let p = parse(Parse::Source, src, &script(Some(3))).unwrap();
    assert_eq!(p.lines[0], "    1  const a = 1;");
    assert_eq!(p.mark, Some(2));
    assert_eq!(p.lines[2], "    3    throw new Error('x');");
    let p = parse(Parse::Source, src, &script(Some(40))).unwrap();
    assert_eq!(p.mark, None);
    assert!(p.note.contains("fewer than 40"));
    assert!(minified(&format!(
        "!function(){{{}}}();",
        "var a=1;".repeat(200)
    )));
    assert!(!minified(src));
    let p = parse(
        Parse::Pretty,
        "function go() {\n  return 1;\n}\n",
        &script(Some(1)),
    )
    .unwrap();
    assert_eq!(p.mark, None);
    assert!(p.note.contains("minified"));
}

#[test]
fn python_bytecode_marks_the_lines_first_instruction() {
    let t = Target::Script(Script {
        lang: crate::codeview_plan::Lang::Python,
        path: None,
        pyc: Some("/srv/app/__pycache__/settings.cpython-312.pyc".into()),
        line: Some(4),
    });
    let p = parse(Parse::PyDis, PYDIS, &t).unwrap();
    assert!(
        p.lines[p.mark.unwrap()]
            .trim_start()
            .starts_with("4           LOAD_CONST")
    );
}
