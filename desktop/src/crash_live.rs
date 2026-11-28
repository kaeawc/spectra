//! Is a crashed process running now? Matched by name against the process list the app already
//! holds (no Spectra call): its pid, how long it has been up, its CPU and memory, and whether it
//! started after its last crash. From there a press opens its page, samples it with the page's
//! own Sample tool, or opens the Timeline around a crash.

use crate::app::App;
use crate::crashes::Section;
use crate::groups::Nav;
use crate::procs::{Process, fmt_cpu, fmt_mem, parse_rfc3339};
use crate::timeline::{SLOW_AFTER, Window};
use crate::tools::Tool;

/// A running process with a crashed process's name.
#[derive(Clone, Debug, PartialEq)]
pub struct Live {
    pub pid: u32,
    /// When it started, in Unix seconds, when that could be read.
    pub started: Option<i64>,
    pub cpu: f64,
    pub rss_kib: f64,
    /// Other running processes with the same name.
    pub others: usize,
}

/// When `p` started: its start time, else how long it had been running at `wall`.
pub fn started(p: &Process, wall: i64) -> Option<i64> {
    parse_rfc3339(&p.started_at).or_else(|| p.elapsed.map(|ns| wall - ns / 1_000_000_000))
}

/// The newest of `procs` named `name` (the one most likely started after the crash), and how
/// many others share the name.
pub fn running(procs: &[Process], name: &str, wall: i64) -> Option<Live> {
    let name = name.trim();
    if name.is_empty() {
        return None;
    }
    let named: Vec<&Process> = procs.iter().filter(|p| p.name() == name).collect();
    let lead = named.iter().max_by_key(|p| (started(p, wall), p.pid))?;
    Some(Live {
        pid: lead.pid,
        started: started(lead, wall),
        cpu: lead.cpu_pct,
        rss_kib: lead.rss_kib,
        others: named.len() - 1,
    })
}

/// `40 s`, `12 min`, `3 h`, `2 days`.
pub fn span(secs: i64) -> String {
    match secs.max(0) {
        s @ 0..60 => format!("{s} s"),
        s @ 60..3600 => format!("{} min", s / 60),
        s @ 3600..172_800 => format!("{} h", s / 3600),
        s => format!("{} days", s / 86_400),
    }
}

/// The live line: `Running now: pid 4821, up 40 min, 2.1% CPU, 140 MB, started 2 min after its
/// last crash`.
pub fn live_line(l: &Live, last_crash: Option<i64>, wall: i64) -> String {
    let mut out = format!("Running now: pid {}", l.pid);
    if let Some(at) = l.started {
        out.push_str(&format!(", up {}", span(wall - at)));
    }
    out.push_str(&format!(", {} CPU, {}", fmt_cpu(l.cpu), fmt_mem(l.rss_kib)));
    match (l.started, last_crash) {
        (Some(up), Some(crash)) if up >= crash => {
            out.push_str(&format!(
                ", started {} after its last crash",
                span(up - crash)
            ));
        }
        (Some(_), Some(_)) => out.push_str(", running since before its last crash"),
        _ => {}
    }
    match l.others {
        0 => {}
        1 => out.push_str(" (1 more running)"),
        n => out.push_str(&format!(" ({n} more running)")),
    }
    out
}

/// The shortest Timeline window that reaches back to `age` seconds ago with a minute to spare,
/// or none when even a week doesn't.
pub fn window_for(age: i64) -> Option<Window> {
    Window::ALL
        .into_iter()
        .find(|w| age.max(0) + SLOW_AFTER <= w.secs())
}

impl App {
    /// The running process named `name`, if any.
    pub fn running_named(&self, name: &str) -> Option<Live> {
        running(&self.procs, name, (self.wall)())
    }

    /// Opens a running crashed process's page, so "back" returns to the Crashes page.
    pub fn open_crashed(
        &mut self,
        section: Section,
        file: &str,
        pid: u32,
        name: &str,
        started: Option<i64>,
    ) {
        if self.still_running(section, file, pid, name, started) {
            self.open(pid);
        }
    }

    /// Whether `pid` is still in the process list under `name` (and the start time it had when
    /// the row was drawn, when both are known); if not, the row says it has exited.
    fn still_running(
        &mut self,
        section: Section,
        file: &str,
        pid: u32,
        name: &str,
        started: Option<i64>,
    ) -> bool {
        let same = self.process(pid).is_some_and(|p| {
            p.name() == name
                && match (started, parse_rfc3339(&p.started_at)) {
                    (Some(was), Some(now)) => was == now,
                    _ => true,
                }
        });
        self.crashes.gone = (!same).then(|| (section, file.to_owned()));
        same
    }

    /// Opens a running crashed process's page and starts its Sample tool there; the page says
    /// why if it can't run.
    pub fn sample_crashed(
        &mut self,
        section: Section,
        file: &str,
        pid: u32,
        name: &str,
        started: Option<i64>,
    ) {
        if self.still_running(section, file, pid, name, started) {
            self.open(pid);
            self.run_tool(Tool::Sample);
        }
    }

    /// Opens the Timeline over the shortest window that holds a crash at `at`, with the rows
    /// around it marked.
    pub fn timeline_at_crash(&mut self, at: i64) {
        let Some(w) = window_for((self.wall)() - at) else {
            return;
        };
        if self.timeline.anchor.is_none() {
            self.timeline.prior_window = Some(self.timeline.window);
        }
        self.timeline.window = w;
        self.timeline.anchor = Some(at);
        self.select(None);
        self.show(Nav::Timeline);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::groups::tests::proc;

    const WALL: i64 = 1_795_176_000; // 2026-11-20T12:00:00Z

    fn at(mut p: Process, started: &str) -> Process {
        p.started_at = started.into();
        p
    }

    #[test]
    fn a_crashed_process_matches_only_a_running_one_with_its_exact_name() {
        let procs = vec![
            at(
                proc(10, "syncbridged", 2.5, 51_200.0),
                "2026-11-20T06:50:00-05:00",
            ),
            proc(11, "syncbridged-helper", 1.0, 1024.0),
        ];
        let l = running(&procs, "syncbridged", WALL).unwrap();
        assert_eq!((l.pid, l.others), (10, 0));
        assert_eq!(l.started, parse_rfc3339("2026-11-20T11:50:00Z"));
        assert_eq!(running(&procs, "Syncbridged", WALL), None);
        assert_eq!(running(&procs, "thumbnailagent", WALL), None);
        assert_eq!(running(&procs, "", WALL), None);
        assert_eq!(running(&[], "syncbridged", WALL), None);
    }

    #[test]
    fn with_several_running_the_newest_leads_and_the_rest_are_counted() {
        let mut no_start = proc(30, "WidgetHostService", 0.0, 1.0);
        no_start.elapsed = Some(3_600 * 1_000_000_000);
        let procs = vec![
            at(
                proc(20, "WidgetHostService", 1.0, 1.0),
                "2026-11-19T09:00:00-05:00",
            ),
            at(
                proc(21, "WidgetHostService", 3.0, 1.0),
                "2026-11-20T06:21:00-05:00",
            ),
            no_start,
            proc(22, "WidgetHostService", 0.0, 1.0),
        ];
        let l = running(&procs, "WidgetHostService", WALL).unwrap();
        // pid 21 started at 11:21Z, after pid 30 (an hour before WALL, 11:00Z), pid 20 (the
        // day before) and pid 22 (no start known).
        assert_eq!((l.pid, l.others), (21, 3));
        assert_eq!(l.cpu, 3.0);
    }

    #[test]
    fn the_live_line_says_how_long_it_has_been_up_since_its_last_crash() {
        let l = Live {
            pid: 4821,
            started: Some(WALL - 40 * 60),
            cpu: 2.14,
            rss_kib: 140.0 * 1024.0,
            others: 0,
        };
        assert_eq!(
            live_line(&l, Some(WALL - 42 * 60), WALL),
            "Running now: pid 4821, up 40 min, 2.1% CPU, 140 MB, started 2 min after its last crash"
        );
        assert_eq!(
            live_line(&l, Some(WALL - 10 * 60), WALL),
            "Running now: pid 4821, up 40 min, 2.1% CPU, 140 MB, running since before its last crash"
        );
        let many = Live {
            started: None,
            others: 2,
            ..l
        };
        assert_eq!(
            live_line(&many, Some(WALL - 60), WALL),
            "Running now: pid 4821, 2.1% CPU, 140 MB (2 more running)"
        );
        assert_eq!(span(3 * 86_400), "3 days");
        assert_eq!(span(26 * 3600), "26 h");
    }

    #[test]
    fn the_timeline_window_is_the_shortest_that_holds_the_crash() {
        assert_eq!(window_for(40 * 60), Some(Window::Hour));
        assert_eq!(window_for(3600), Some(Window::Hours6), "a minute to spare");
        assert_eq!(window_for(5 * 3600), Some(Window::Hours6));
        assert_eq!(window_for(20 * 3600), Some(Window::Day));
        assert_eq!(window_for(3 * 86_400), Some(Window::Week));
        assert_eq!(window_for(8 * 86_400), None);
        assert_eq!(window_for(-30), Some(Window::Hour));
    }
}
