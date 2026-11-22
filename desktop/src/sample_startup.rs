//! Sample startup items for the Startup page's headless scenes and tests: a real listing from a
//! Mac, trimmed (tests/fixtures/startup), the confirmation before disabling a running agent, and
//! a read that failed.

use crate::app::App;
use crate::groups::Nav;
use crate::sample::sample_scene;
use crate::startup::{Action, Listing, merge, parse_services, parse_startup};

pub const LIST: &str = include_str!("../tests/fixtures/startup/startup-list.json");
pub const SERVICES: &str = include_str!("../tests/fixtures/startup/services.json");

/// What `spectra startup disable com.github.domt4.homebrew-autoupdate --json` prints when it
/// works, in the shape of Spectra's `spectra.startup.change` result.
#[cfg(test)]
pub const DISABLED_JSON: &str = r#"{
  "action": "disable",
  "label": "com.github.domt4.homebrew-autoupdate",
  "domain": "gui/501",
  "refused": false,
  "commands": [
    {"argv": ["launchctl", "disable", "gui/501/com.github.domt4.homebrew-autoupdate"], "exit_code": 0}
  ],
  "before": {"label": "com.github.domt4.homebrew-autoupdate", "kind": "launch_agent", "scope": "user", "disabled": false, "loaded": false, "running": false, "sources": ["launchd_plist"]},
  "after": {"label": "com.github.domt4.homebrew-autoupdate", "kind": "launch_agent", "scope": "user", "disabled": true, "disabled_by": "launchctl override", "loaded": false, "running": false, "sources": ["launchd_plist"]}
}"#;

/// What it prints when a second plist defines the label (exit 3): nothing ran.
#[cfg(test)]
pub const REFUSED_JSON: &str = r#"{
  "action": "disable",
  "label": "com.github.domt4.homebrew-autoupdate",
  "refused": true,
  "reason": "duplicate_label",
  "message": "refusing to disable com.github.domt4.homebrew-autoupdate: the label is defined by 2 plists (/Users/me/Library/LaunchAgents/a.plist, /Users/me/Library/LaunchAgents/b.plist) and launchd loads only one; remove or rename the extra plist first",
  "commands": []
}"#;

/// The trimmed listing, as a read lands it.
pub fn listing() -> Listing {
    let (items, notes) = parse_startup(LIST).expect("the startup fixture reads");
    let services = parse_services(SERVICES).expect("the services fixture reads");
    Listing {
        items: merge(items, services),
        notes,
        apple: false,
    }
}

/// The page as the sidebar opens it, without running Spectra.
fn startup_page(s: &mut App) {
    s.select(None);
    s.nav = Nav::Startup;
}

/// What starts at login on a Mac: apps with their login items, agents and daemons; the
/// person's own agents (one of them disabled); and background services.
pub fn startup_scene(s: &mut App) {
    sample_scene(s);
    let mut l = listing();
    if let Some(i) = l
        .items
        .iter_mut()
        .find(|i| i.label == "com.github.domt4.homebrew-autoupdate")
    {
        i.disabled = true;
    }
    s.startup.listing = Some(l);
    startup_page(s);
}

/// The confirmation before disabling watchman, which is running.
pub fn startup_confirm_scene(s: &mut App) {
    startup_scene(s);
    s.ask_startup(
        "com.github.facebook.watchman",
        "/Users/me/Library/LaunchAgents/com.github.facebook.watchman.plist",
        Action::Disable,
    );
}

/// A read that failed before any listing landed.
pub fn startup_failed_scene(s: &mut App) {
    sample_scene(s);
    s.startup.error = Some("spectra took longer than 60 s and was stopped".into());
    startup_page(s);
}
