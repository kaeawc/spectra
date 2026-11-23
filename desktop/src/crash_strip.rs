//! The Crashes page's strip of crashes over time, from the times the listings already read: one
//! bar per slice of the span (the last 7 days, or back to the oldest report when that is
//! older), as tall as the reports in it. Dragging across it (or pressing one bar) picks a window
//! the lists below then keep to; Clear goes back to everything.

use crate::app::App;
use crate::crashes::{Crashes, MemoryKill, Report, Signature};
use rune::{Drag, Phase};

/// Bars across the strip: three hours each over a week.
pub const BUCKETS: usize = 56;
/// How far back the strip reaches at least, in seconds.
pub const SPAN: i64 = 7 * 86_400;

/// The strip: where it starts, how long each bar is, and how many reports each holds.
#[derive(Clone, Debug, PartialEq)]
pub struct Strip {
    pub start: i64,
    pub step: i64,
    pub counts: Vec<u32>,
}

/// Every report's time, once: the recent reports and the memory kills (one that is both counts
/// once). With neither, the signatures' latest times stand in.
pub fn crash_times(c: &Crashes) -> Vec<i64> {
    let mut seen = std::collections::HashSet::new();
    let mut out = Vec::new();
    let reports = c.reports.iter().map(|r| (r.at, r.file.as_str()));
    let kills = c.kills.iter().map(|k| (k.at, k.file.as_str()));
    for (at, file) in reports.chain(kills) {
        let Some(at) = at else { continue };
        if file.is_empty() || seen.insert(file) {
            out.push(at);
        }
    }
    if out.is_empty() {
        out.extend(c.signatures.iter().filter_map(|s| s.last));
    }
    out
}

impl Strip {
    /// Bars over the last [`SPAN`] to `wall`, reaching back to the oldest of `times` if older.
    pub fn of(times: &[i64], wall: i64) -> Strip {
        let start = times.iter().copied().fold(wall - SPAN, i64::min);
        let span = (wall - start).max(1);
        let step = (span + BUCKETS as i64 - 1) / BUCKETS as i64;
        let mut s = Strip {
            start,
            step: step.max(1),
            counts: vec![0; BUCKETS],
        };
        for t in times {
            let i = s.bucket(*t);
            s.counts[i] += 1;
        }
        s
    }

    /// The bar `at` falls in; a time past the end lands in the last.
    pub fn bucket(&self, at: i64) -> usize {
        (((at - self.start).max(0) / self.step) as usize).min(BUCKETS - 1)
    }

    /// The bar under a pointer `x` points from the left of bar `from`, each bar `w` wide: a drag
    /// that started on one bar reads where it went from there.
    pub fn bucket_at(from: usize, x: f32, w: f32) -> usize {
        if w <= 0.0 {
            return from.min(BUCKETS - 1);
        }
        let i = from as f32 + (x / w).floor();
        (i.max(0.0) as usize).min(BUCKETS - 1)
    }

    /// The window bars `a` through `b` cover, either way round: from the first's start to the
    /// last's end.
    pub fn window(&self, a: usize, b: usize) -> (i64, i64) {
        let (lo, hi) = (a.min(b), a.max(b).min(BUCKETS - 1));
        (
            self.start + lo as i64 * self.step,
            self.start + (hi as i64 + 1) * self.step,
        )
    }

    /// Reports in bars `a` through `b`.
    pub fn count(&self, a: usize, b: usize) -> u32 {
        let (lo, hi) = (a.min(b), a.max(b).min(BUCKETS - 1));
        self.counts[lo..=hi].iter().sum()
    }

    /// Where the last bar ends.
    pub fn end(&self) -> i64 {
        self.start + BUCKETS as i64 * self.step
    }

    pub fn max(&self) -> u32 {
        self.counts.iter().copied().max().unwrap_or(0)
    }
}

/// Whether a time is inside `window` (from inclusive, to exclusive); everything is without one,
/// and a row without a time is outside any. A window that reaches the strip's `end` keeps every
/// later time too, as the last bar counts them.
pub fn within(at: Option<i64>, window: Option<(i64, i64)>, end: i64) -> bool {
    match (window, at) {
        (None, _) => true,
        (Some((from, to)), Some(t)) => t >= from && (t < to || to >= end),
        (Some(_), None) => false,
    }
}

/// Whether a signature's crashes, first to last, could include one inside `window`.
pub fn overlaps(s: &Signature, window: Option<(i64, i64)>, end: i64) -> bool {
    let Some((from, to)) = window else {
        return true;
    };
    match (s.first.or(s.last), s.last.or(s.first)) {
        (Some(first), Some(last)) => (first < to || to >= end) && last >= from,
        _ => false,
    }
}

/// The lists, kept to `window`.
pub fn filtered(c: &Crashes, window: Option<(i64, i64)>, end: i64) -> Crashes {
    if window.is_none() {
        return c.clone();
    }
    Crashes {
        signatures: c
            .signatures
            .iter()
            .filter(|s| overlaps(s, window, end))
            .cloned()
            .collect::<Vec<Signature>>(),
        reports: c
            .reports
            .iter()
            .filter(|r| within(r.at, window, end))
            .cloned()
            .collect::<Vec<Report>>(),
        kills: c
            .kills
            .iter()
            .filter(|k| within(k.at, window, end))
            .cloned()
            .collect::<Vec<MemoryKill>>(),
        reports_error: c.reports_error.clone(),
        kills_error: c.kills_error.clone(),
    }
}

impl App {
    /// The strip for the page's reading, if there is one.
    pub fn crash_strip(&self) -> Option<Strip> {
        let (data, _) = self.crashes.data.as_ref()?;
        Some(Strip::of(&crash_times(data), (self.wall)()))
    }

    /// Keeps the lists to bars `a` through `b`.
    pub fn pick_crash_window(&mut self, a: usize, b: usize) {
        if let Some(strip) = self.crash_strip() {
            self.crashes.range = Some(strip.window(a, b));
        }
        self.crashes.drag = None;
    }

    /// Back to every crash.
    pub fn clear_crash_window(&mut self) {
        self.crashes.range = None;
        self.crashes.drag = None;
    }

    /// A drag across the strip: it shows the bars it covers as it moves and keeps the lists to
    /// them when it lets go; a press without moving picks one bar. A cancelled drag changes
    /// nothing.
    pub fn drag_crash_strip(&mut self, from: usize, d: &Drag) {
        let i = Strip::bucket_at(from, d.x, d.w);
        match d.phase {
            Phase::Down => self.crashes.drag = Some((i, i)),
            Phase::Move => {
                if let Some((a, _)) = self.crashes.drag {
                    self.crashes.drag = Some((a, i));
                }
                self.crashes.hover = Some(i);
            }
            Phase::Up => match self.crashes.drag.take() {
                Some((a, _)) if !d.cancelled() => self.pick_crash_window(a, i),
                _ => {}
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::crashes::tests::{LIST, OOM, SIGNATURES};
    use crate::crashes::{parse_kills, parse_reports, parse_signatures};
    use crate::procs::parse_rfc3339;

    fn t(s: &str) -> i64 {
        parse_rfc3339(s).unwrap()
    }

    fn fixture() -> Crashes {
        Crashes {
            signatures: parse_signatures(SIGNATURES).unwrap(),
            reports: parse_reports(LIST).unwrap(),
            kills: parse_kills(OOM).unwrap(),
            ..Crashes::default()
        }
    }

    const WALL: i64 = 1_795_176_000; // 2026-11-20T12:00:00Z

    #[test]
    fn times_count_each_report_once() {
        let c = fixture();
        let times = crash_times(&c);
        // Six timed reports, plus the low-memory kill; notehelper's report is in both lists.
        assert_eq!(times.len(), 7);
        let only_sigs = Crashes {
            signatures: c.signatures.clone(),
            ..Crashes::default()
        };
        assert_eq!(crash_times(&only_sigs).len(), 6, "signatures stand in");
        assert!(crash_times(&Crashes::default()).is_empty());
    }

    #[test]
    fn bars_cover_the_last_week_in_three_hour_slices() {
        let s = Strip::of(&[WALL - 60, WALL - 61, WALL - 4 * 3600, WALL + 30], WALL);
        assert_eq!(s.start, WALL - SPAN);
        assert_eq!(s.step, 3 * 3600);
        assert_eq!(s.counts.len(), BUCKETS);
        assert_eq!(
            s.counts[BUCKETS - 1],
            3,
            "the last bar, and one a little past now"
        );
        assert_eq!(s.counts[BUCKETS - 2], 1);
        assert_eq!(s.max(), 3);
        assert_eq!(s.counts.iter().sum::<u32>(), 4);
        assert_eq!(s.count(BUCKETS - 2, BUCKETS - 1), 4);
    }

    #[test]
    fn an_older_report_stretches_the_strip_back_to_it() {
        let old = WALL - 14 * 86_400;
        let s = Strip::of(&[old, WALL - 10], WALL);
        assert_eq!(s.start, old);
        assert_eq!(s.step, 6 * 3600);
        assert_eq!(s.counts[0], 1);
        assert_eq!(s.counts[BUCKETS - 1], 1);
        assert_eq!(Strip::of(&[], WALL).counts.iter().sum::<u32>(), 0);
    }

    #[test]
    fn a_window_runs_from_the_first_bars_start_to_the_last_bars_end_either_way() {
        let s = Strip::of(&[], WALL);
        assert_eq!(s.window(2, 4), (s.start + 2 * s.step, s.start + 5 * s.step));
        assert_eq!(s.window(4, 2), s.window(2, 4));
        assert_eq!(
            s.window(BUCKETS - 1, BUCKETS + 5).1,
            s.start + BUCKETS as i64 * s.step
        );
        // Bars 10 wide; a drag from bar 5.
        assert_eq!(Strip::bucket_at(5, 3.0, 10.0), 5);
        assert_eq!(Strip::bucket_at(5, 25.0, 10.0), 7);
        assert_eq!(Strip::bucket_at(5, -0.5, 10.0), 4);
        assert_eq!(Strip::bucket_at(5, -999.0, 10.0), 0);
        assert_eq!(Strip::bucket_at(5, 9999.0, 10.0), BUCKETS - 1);
        assert_eq!(Strip::bucket_at(5, 10.0, 0.0), 5);
    }

    #[test]
    fn the_lists_keep_to_the_window() {
        let c = fixture();
        // 05:00 to 06:30 New York on Nov 20.
        let w = Some((t("2026-11-20T10:00:00Z"), t("2026-11-20T11:30:00Z")));
        let f = filtered(&c, w, i64::MAX);
        let names = |v: Vec<&str>| v.join(",");
        assert_eq!(
            names(f.reports.iter().map(|r| r.process.as_str()).collect()),
            "WidgetHostService,Sketchpad,thumbnailagent"
        );
        assert!(f.kills.is_empty());
        // Any signature whose first-to-last span reaches into the window.
        assert_eq!(
            names(f.signatures.iter().map(|s| s.process.as_str()).collect()),
            "syncbridged,WidgetHostService,thumbnailagent"
        );
        assert_eq!(filtered(&c, None, i64::MAX), c);
        assert!(within(Some(5), Some((5, 6)), 100));
        assert!(!within(Some(6), Some((5, 6)), 100), "the end is outside");
        assert!(!within(None, Some((5, 6)), 100));
        assert!(within(None, None, 100));
    }

    #[test]
    fn a_report_after_the_clock_is_in_the_last_bar_and_in_its_window() {
        let late = WALL + 90;
        let s = Strip::of(&[late], WALL);
        assert_eq!(s.counts[BUCKETS - 1], 1);
        let w = Some(s.window(BUCKETS - 1, BUCKETS - 1));
        assert!(within(Some(late), w, s.end()));
        assert!(
            within(Some(s.end()), w, s.end()),
            "the last end is inclusive"
        );
        assert!(
            within(Some(s.end() + 5_000), w, s.end()),
            "far future clamps in"
        );
        // Earlier windows still end before their last second.
        let early = s.window(0, 0);
        assert!(!within(Some(early.1), Some(early), s.end()));
    }
}
