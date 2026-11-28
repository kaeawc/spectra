//! Sample scenes for the code view, from invented tool output (tests/fixtures/codeview): a
//! crash frame in a closed framework disassembled by lldb with its offset marked, a JVM frame's
//! bytecode from javap with its line marked, and a frame with no code view.

use crate::app::App;
use crate::codeview::{Panel, Shown, State};
use crate::codeview_parse::parse;
use crate::codeview_plan::{Jvm, Native, Parse, Request, Target, plan};
use crate::codeview_tools::Tools;

pub const LLDB: &str = include_str!("../tests/fixtures/codeview/lldb-native.txt");
pub const JAVAP: &str = include_str!("../tests/fixtures/codeview/javap.txt");

/// The frame the native scene shows, from the crash fixture.
pub const FRAME: &str = "WidgetKitCore`TimelineCache.entry(for:) + 344";
const PROCESS: &str = "/System/Library/PrivateFrameworks/WidgetHost.framework/XPCServices/WidgetHostService.xpc/WidgetHostService";
pub const JAVA_FRAME: &str =
    "com.acme.checkout.inventory.StockLedger.reserve(StockLedger.java:212)";

fn open(s: &mut App, request: Request, state: State) {
    s.codeview.open = Some(Panel {
        title: request.title(),
        request,
        origin: s.origin(),
        state,
        show_all: false,
    });
}

fn shown(p: Parse, tool: &str, out: &str, t: &Target) -> State {
    let r = parse(p, out, t).expect("the fixture parses");
    State::Shown(Shown {
        what: p.what(tool),
        lines: r.lines,
        mark: r.mark,
        note: r.note,
        tried: Vec::new(),
    })
}

/// The widget host's crashed frame in its (made-up) closed framework, disassembled.
pub fn codeview_native_scene(s: &mut App) {
    crate::sample_crashes::crashes_report_scene(s);
    let t = Target::Native(Native {
        image: "WidgetKitCore".into(),
        names: vec!["TimelineCache.entry(for:)".into(), "entry".into()],
        offset: Some(344),
        binary: None,
        host: Some(PROCESS.into()),
    });
    let state = shown(Parse::Lldb, "lldb", LLDB, &t);
    open(
        s,
        Request::Crash {
            frame: FRAME.into(),
            process_path: PROCESS.into(),
        },
        state,
    );
}

/// A checkout JVM's stock ledger frame as javap's bytecode, line 212 marked; a decompiler was
/// tried first and couldn't find the class.
pub fn codeview_jvm_scene(s: &mut App) {
    crate::sample_jvm::tool_threads_scene(s);
    let t = Target::Jvm(Jvm {
        class: "com.acme.checkout.inventory.StockLedger".into(),
        method: "reserve".into(),
        line: Some(212),
        jdk: false,
        classpath: Vec::new(),
        at: None,
        javap: None,
    });
    let mut state = shown(Parse::Javap, "javap", JAVAP, &t);
    if let State::Shown(sh) = &mut state {
        sh.tried = vec!["CFR: the class isn't in a jar or folder on the JVM's classpath".into()];
    }
    open(
        s,
        Request::Java {
            frame: JAVA_FRAME.into(),
            command_line: String::new(),
            exe: String::new(),
        },
        state,
    );
}

/// The widget host's own code, which has no symbol, on a Mac without lldb.
pub fn codeview_none_scene(s: &mut App) {
    crate::sample_crashes::crashes_report_scene(s);
    let t = Target::Native(Native {
        image: "WidgetHostService".into(),
        names: Vec::new(),
        offset: Some(0x1a2c4),
        binary: Some(PROCESS.into()),
        host: None,
    });
    let why = plan(&t, &Tools::default()).expect_err("nothing runs without tools");
    open(
        s,
        Request::Crash {
            frame: "WidgetHostService + 0x1a2c4".into(),
            process_path: PROCESS.into(),
        },
        State::Nothing(why.reason),
    );
}
