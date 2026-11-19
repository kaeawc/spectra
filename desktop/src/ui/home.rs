//! "All JVMs": every finding on the machine, worst first.

use rune::{El, col};
use rune_kit::Kit;

use super::card::{card, notice};
use crate::app::App;
use crate::findings::Finding;

pub fn home(s: &App, findings: &[Finding], k: &Kit) -> El {
    let mut list = col().key("findings").w_full().padding(24.0).gap(10.0);
    if let Some(e) = &s.error {
        list = list.child(notice("Couldn't reach Spectra", e, k));
    } else if !s.loaded {
        list = list.child(notice(
            "Looking for JVMs",
            "Spectra is reading every running JVM.",
            k,
        ));
    } else if s.jvms.is_empty() {
        list = list.child(notice(
            "No JVMs running",
            "Start one and it shows up here, with nothing to set up.",
            k,
        ));
    }
    findings
        .iter()
        .fold(list, |list, f| list.child(card(s, f, k)))
}
