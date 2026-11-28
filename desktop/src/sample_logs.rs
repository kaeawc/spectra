//! Sample data for a page's Logs: rust-analyzer's recent entries with an error and a fault, a
//! shell whose name five processes share, and a query that ran out of time.

use crate::app::App;
use crate::groups::Nav;
use crate::inspect::RunState;
use crate::logs::{self, Level, Window};
use crate::sample::{sample_live, sample_scene};
use crate::tools::{Report, Tool};

/// What `spectra logs --json --process rust-analyzer --last 15m` printed, oldest first.
pub const ANALYZER_LOGS: &str = r#"{"entries":[
  {"timestamp":"2026-11-20T06:52:10.114201-05:00","process":"rust-analyzer","pid":1300,"sender":"libsystem_info.dylib","subsystem":"com.apple.network","category":"resolver","event_type":"logEvent","message_type":"Default","event_message":"nw_resolver_start_query_timer Starting timer for query crates.io"},
  {"timestamp":"2026-11-20T06:53:41.502930-05:00","process":"rust-analyzer","pid":1300,"sender":"CoreFoundation","subsystem":"com.apple.CFBundle","category":"resources","event_type":"logEvent","message_type":"Info","event_message":"Resource lookup at CFBundle 0x6000 </Users/me/.cargo/bin> (not loaded) took 0.4 ms"},
  {"timestamp":"2026-11-20T06:55:02.880412-05:00","process":"rust-analyzer","pid":1300,"sender":"FSEvents","subsystem":"com.apple.fsevents","category":"client","event_type":"logEvent","message_type":"Error","event_message":"FSEventStreamStart: register_with_server: ERROR: f2d_register_rpc() => (null) (-21)"},
  {"timestamp":"2026-11-20T06:57:19.003318-05:00","process":"rust-analyzer","pid":1300,"sender":"rust-analyzer","event_type":"activityCreateEvent","event_message":"flycheck"},
  {"timestamp":"2026-11-20T06:58:44.671095-05:00","process":"rust-analyzer","pid":1300,"sender":"libxpc.dylib","subsystem":"com.apple.xpc","category":"connection","event_type":"logEvent","message_type":"Fault","event_message":"[0x7f8: com.apple.FSEvents] Connection invalidated while a message was in flight; the reply will never arrive"},
  {"timestamp":"2026-11-20T06:59:30.250117-05:00","process":"rust-analyzer","pid":1300,"sender":"CoreFoundation","subsystem":"com.apple.defaults","category":"User Defaults","event_type":"logEvent","message_type":"Info","event_message":"found no value for key NSApplicationCrashOnExceptions in CFPrefsSource<0x6000> (Domain: rust-analyzer, Container: (null))"},
  {"timestamp":"0001-01-01T00:00:00Z","process":".","sender":"."}
],"clock":{},"query_duration":2310000000}"#;

/// rust-analyzer's page (pid 1300).
fn analyzer(s: &mut App) {
    sample_scene(s);
    s.expanded = None;
    s.show(Nav::All);
    s.open(1300);
    sample_live(s);
}

/// rust-analyzer's last 15 minutes, errors and faults standing out, Follow on.
pub fn logs_scene(s: &mut App) {
    analyzer(s);
    let report = Tool::Logs
        .parse(ANALYZER_LOGS)
        .expect("the sample logs parse");
    let page = &mut s.inspect.logs;
    page.window = Window::Min15;
    page.follow = true;
    page.state = Some(RunState::Done(Box::new(report)));
}

/// A shell five processes are named after: only its own entries count, and it has none at
/// Error or above.
pub fn logs_empty_scene(s: &mut App) {
    sample_scene(s);
    s.expanded = None;
    s.show(Nav::All);
    s.open(1101);
    let page = &mut s.inspect.logs;
    page.level = Level::Error;
    page.state = Some(RunState::Done(Box::new(Report::Logs(logs::LogsReport {
        others: 3,
        ..Default::default()
    }))));
}

/// An hour's query that ran out of time.
pub fn logs_timeout_scene(s: &mut App) {
    analyzer(s);
    let why = crate::procs::run_failure("spectra", crate::run::RunError::Timeout(logs::DEADLINE));
    let page = &mut s.inspect.logs;
    page.window = Window::Hour;
    page.state = Some(RunState::Failed(logs::explain(&why)));
}
