//! Sample data for the Toolchains page's headless scenes and tests, from invented fixtures in the
//! shape of `spectra toolchain --json` (tests/fixtures/toolchains), with paths under
//! `/Users/user`: a Mac with three JDKs (two of them Java 17), runtimes and Homebrew, a search,
//! a Mac with nothing found, a failed read and a read on its way.

use rune::TextEdit;

use crate::app::App;
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::toolchains::parse;

pub const FULL: &str = include_str!("../tests/fixtures/toolchains/toolchains.json");
#[cfg(test)]
pub const SPARSE: &str = include_str!("../tests/fixtures/toolchains/sparse.json");
pub const EMPTY: &str = include_str!("../tests/fixtures/toolchains/empty.json");

fn page(s: &mut App) {
    sample_scene(s);
    s.select(None);
    s.nav = Nav::Toolchains;
    // The sample's JAVA_HOME is there; no scene reads the disk.
    s.toolchains.exists = |_| true;
}

fn read(s: &mut App, json: &str) {
    let mut inv = parse(json).expect("the toolchains fixture reads");
    inv.check_java_home(|_| true);
    s.toolchains.data = Some((inv, s.now - 45.0));
}

/// Everything found: Java first with the Java 17 flag, then the runtimes; Homebrew closed.
pub fn toolchains_scene(s: &mut App) {
    page(s);
    read(s, FULL);
}

/// A search for "17": the sections with a match open, the rest are out.
pub fn toolchains_filtered_scene(s: &mut App) {
    page(s);
    read(s, FULL);
    s.toolchains.query = TextEdit::new("17");
}

/// Spectra found nothing at all.
pub fn toolchains_empty_scene(s: &mut App) {
    page(s);
    read(s, EMPTY);
}

/// The read failed, with nothing read before it.
pub fn toolchains_failed_scene(s: &mut App) {
    page(s);
    s.toolchains.error =
        Some("This Spectra has no `spectra toolchain --json`. Update Spectra to use it.".into());
}

/// The first read on its way.
pub fn toolchains_reading_scene(s: &mut App) {
    page(s);
    s.toolchains.sample_reading();
}
