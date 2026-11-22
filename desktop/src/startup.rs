//! What starts at login or boot, and the background services launchd runs: read from
//! `spectra startup list` and `spectra services`, grouped by the app they belong to, and
//! changed (disable or enable) only for the person's own launch agents, through
//! `spectra startup disable|enable`, one confirmed press at a time.

use std::cmp::Ordering;
use std::process::Command;
use std::sync::atomic::AtomicBool;
use std::sync::mpsc::{Receiver, Sender, channel};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use rune::TextEdit;
use serde::Deserialize;

use crate::app::App;
use crate::run::{Ended, run_until};

/// How long a listing may take. Spectra's own limit is two minutes; a listing takes a second or
/// two, a few with Apple's items.
pub const LIST_DEADLINE: Duration = Duration::from_secs(60);
/// How long a disable or enable may take. Longer than Spectra's own two-minute limit, so Spectra
/// always ends a change itself and this never kills one half done.
pub const CHANGE_DEADLINE: Duration = Duration::from_secs(150);
/// The most a command may print.
const MAX_OUTPUT: usize = 16 << 20;
/// Rows drawn before "and N more": Apple's items alone are nearly a thousand.
pub const MAX_ROWS: usize = 200;

/// Set when the app quits: a listing in flight is killed. A change is not (see [`cancel_for`]).
pub static CANCEL: AtomicBool = AtomicBool::new(false);

/// Never set: what a change in flight watches instead of [`CANCEL`].
static NEVER: AtomicBool = AtomicBool::new(false);

/// The flag that stops a run of `spectra <args>`. A read stops at quit; a disable or enable
/// doesn't, since killing it can leave an agent disabled but still loaded. Quit waits a moment
/// for it, then leaves it running to finish on its own.
pub fn cancel_for(args: &[String]) -> &'static AtomicBool {
    let read = args.first().map(String::as_str) != Some("startup")
        || args.get(1).map(String::as_str) == Some("list");
    if read { &CANCEL } else { &NEVER }
}

/// What a command printed and how it exited. A change prints its JSON even when it was
/// refused (exit 3) or didn't take (exit 1), so stdout is kept whatever the exit.
#[derive(Clone, Debug, Default)]
pub struct Ran {
    pub code: Option<i32>,
    pub stdout: String,
    pub stderr: String,
}

impl Ran {
    /// Spectra's first line on stderr, if it said anything.
    fn said(&self) -> Option<&str> {
        self.stderr.lines().map(str::trim).find(|l| !l.is_empty())
    }
}

/// Runs `spectra <args>`. Tests swap in a fake that records the args and runs nothing.
pub trait Runner: Send + Sync {
    fn run(&self, args: &[String], deadline: Duration) -> Result<Ran, String>;
}

/// The real Spectra, from `SPECTRA_BIN` or `PATH`, with a deadline and an output cap.
pub struct SpectraBin;

impl Runner for SpectraBin {
    fn run(&self, args: &[String], deadline: Duration) -> Result<Ran, String> {
        let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
        let mut cmd = Command::new(&bin);
        cmd.args(args);
        let out = run_until(cmd, deadline, MAX_OUTPUT, cancel_for(args)).map_err(|e| match e {
            Ended::Cancelled => format!("{bin} was stopped"),
            Ended::Failed(e) => crate::procs::run_failure(&bin, e),
        })?;
        Ok(Ran {
            code: out.status.code(),
            stdout: String::from_utf8_lossy(&out.stdout).into_owned(),
            stderr: String::from_utf8_lossy(&out.stderr).into_owned(),
        })
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Kind {
    LoginItem,
    LaunchAgent,
    LaunchDaemon,
    /// A launchd job `spectra services` lists that isn't a startup item.
    Service,
}

impl Kind {
    fn of(s: &str) -> Kind {
        match s {
            "login_item" => Kind::LoginItem,
            "launch_daemon" => Kind::LaunchDaemon,
            _ => Kind::LaunchAgent,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Kind::LoginItem => "Login item",
            Kind::LaunchAgent => "Launch agent",
            Kind::LaunchDaemon => "Launch daemon",
            Kind::Service => "Background service",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Scope {
    /// The person's own: changeable without an administrator.
    User,
    /// Installed for every user: changing it needs root.
    System,
}

/// When launchd starts it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Trigger {
    Login,
    Boot,
    KeepAlive,
    Interval,
    Calendar,
    OnDemand,
    Other(String),
}

impl Trigger {
    fn of(s: &str) -> Trigger {
        match s {
            "login" => Trigger::Login,
            "boot" => Trigger::Boot,
            "keep_alive" => Trigger::KeepAlive,
            "interval" => Trigger::Interval,
            "calendar" => Trigger::Calendar,
            "on_demand" => Trigger::OnDemand,
            other => Trigger::Other(other.replace('_', " ")),
        }
    }

    pub fn label(&self) -> String {
        match self {
            Trigger::Login => "At login".into(),
            Trigger::Boot => "At boot".into(),
            Trigger::KeepAlive => "Kept alive".into(),
            Trigger::Interval => "On an interval".into(),
            Trigger::Calendar => "On a schedule".into(),
            Trigger::OnDemand => "On demand".into(),
            Trigger::Other(s) => s.clone(),
        }
    }
}

/// The app an item belongs to.
#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
#[serde(default)]
pub struct Owner {
    pub app: String,
    pub bundle_path: String,
    pub bundle_id: String,
    pub developer: String,
}

impl Owner {
    /// What tells one app from another: its bundle, else its name.
    fn key(&self) -> &str {
        if self.bundle_path.is_empty() {
            &self.app
        } else {
            &self.bundle_path
        }
    }

    pub fn name(&self) -> &str {
        if self.app.is_empty() {
            self.key()
        } else {
            &self.app
        }
    }
}

/// One thing that starts at login or boot, or one background service.
#[derive(Clone, Debug, PartialEq)]
pub struct Item {
    pub label: String,
    pub name: String,
    pub kind: Kind,
    pub scope: Scope,
    pub program: String,
    pub plist_path: String,
    pub triggers: Vec<Trigger>,
    pub disabled: bool,
    pub loaded: bool,
    pub running: bool,
    pub pid: Option<u32>,
    pub apple: bool,
    pub owner: Option<Owner>,
    /// More than one plist defines its label.
    pub duplicate: bool,
}

impl Item {
    pub fn display_name(&self) -> &str {
        if self.name.is_empty() {
            &self.label
        } else {
            &self.name
        }
    }

    /// The triggers in words.
    pub fn trigger_text(&self) -> String {
        if self.triggers.is_empty() {
            return Trigger::OnDemand.label();
        }
        self.triggers
            .iter()
            .map(Trigger::label)
            .collect::<Vec<_>>()
            .join(" · ")
    }

    pub fn is_running(&self) -> bool {
        self.running || self.pid.is_some()
    }

    /// Why Disable / Enable is off for this item, or `None` when it can be changed: only the
    /// person's own launch agents, which Spectra changes without an administrator.
    pub fn change_refusal(&self) -> Option<&'static str> {
        if self.kind == Kind::Service {
            return Some("Not a startup item; Spectra Desktop changes only your own launch agents");
        }
        if self.scope == Scope::System || self.kind == Kind::LaunchDaemon {
            return Some("Needs an administrator; Spectra Desktop doesn't run sudo");
        }
        if self.kind == Kind::LoginItem {
            return Some("Change it in System Settings › General › Login Items");
        }
        if self.apple || self.label.starts_with("com.apple.") {
            return Some("An Apple item; Spectra Desktop leaves these alone");
        }
        if self.duplicate {
            return Some(
                "More than one plist uses this label, so a change can't say which it would act on",
            );
        }
        if self.plist_path.is_empty() {
            return Some("No launch agent plist to change");
        }
        None
    }

    fn matches(&self, q: &str) -> bool {
        if q.is_empty() {
            return true;
        }
        let has = |s: &str| s.to_lowercase().contains(q);
        has(&self.label)
            || has(&self.name)
            || has(&self.program)
            || has(self.kind.label())
            || self.pid.is_some_and(|p| p.to_string() == q)
            || self
                .owner
                .as_ref()
                .is_some_and(|o| has(&o.app) || has(&o.bundle_id) || has(&o.developer))
    }
}

/// One read of both commands.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Listing {
    pub items: Vec<Item>,
    /// What Spectra couldn't read, in words: a source that needs root, unreadable plists, a
    /// failed `spectra services`.
    pub notes: Vec<String>,
    /// Read with `--include-apple`.
    pub apple: bool,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct ItemJson {
    label: String,
    name: String,
    kind: String,
    scope: String,
    program: String,
    program_arguments: Vec<String>,
    plist_path: String,
    trigger: String,
    triggers: Vec<String>,
    disabled: bool,
    loaded: bool,
    running: bool,
    pid: i64,
    apple: bool,
    owner: Option<Owner>,
    duplicate_of: Vec<String>,
}

impl ItemJson {
    fn item(self) -> Item {
        let mut triggers: Vec<Trigger> = self.triggers.iter().map(|t| Trigger::of(t)).collect();
        if triggers.is_empty() && !self.trigger.is_empty() {
            triggers.push(Trigger::of(&self.trigger));
        }
        let program = if self.program.is_empty() {
            self.program_arguments.first().cloned().unwrap_or_default()
        } else {
            self.program
        };
        Item {
            apple: self.apple
                || self.label.starts_with("com.apple.")
                || self.plist_path.starts_with("/System/"),
            label: self.label,
            name: self.name,
            kind: Kind::of(&self.kind),
            scope: if self.scope == "user" {
                Scope::User
            } else {
                Scope::System
            },
            program,
            plist_path: self.plist_path,
            triggers,
            disabled: self.disabled,
            loaded: self.loaded,
            running: self.running,
            pid: u32::try_from(self.pid).ok().filter(|p| *p > 0),
            owner: self
                .owner
                .filter(|o| !o.app.is_empty() || !o.bundle_path.is_empty()),
            duplicate: !self.duplicate_of.is_empty(),
        }
    }
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct SourceJson {
    name: String,
    status: String,
    detail: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct PlistErrorJson {
    path: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct ReportJson {
    items: Vec<ItemJson>,
    sources: Vec<SourceJson>,
    errors: Vec<PlistErrorJson>,
}

/// A source's name in words.
fn source_name(name: &str) -> String {
    match name {
        "launchd_plist" => "Launch agent and daemon plists".into(),
        "launchctl_print_disabled" => "launchd's disabled list".into(),
        "launchctl_print" => "launchd's loaded jobs".into(),
        "btm" | "btm_store" => "Login Items".into(),
        "owner" => "Which app owns each item".into(),
        other => other.replace('_', " "),
    }
}

/// Reads `spectra startup list --json`.
pub fn parse_startup(json: &str) -> Result<(Vec<Item>, Vec<String>), String> {
    let r: ReportJson = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's startup list didn't read as JSON: {e}"))?;
    let mut notes = Vec::new();
    // BTM is read from its store; `btm` (the root fallback) is skipped then, which isn't news.
    let btm_ok = r
        .sources
        .iter()
        .any(|s| s.name.starts_with("btm") && s.status == "ok");
    for s in &r.sources {
        let why = match s.status.as_str() {
            "ok" | "skipped" | "" => continue,
            _ if btm_ok && s.name.starts_with("btm") => continue,
            "needs_root" => "need an administrator to read".to_owned(),
            _ if !s.detail.is_empty() => format!("couldn't be read: {}", s.detail),
            _ => "couldn't be read".to_owned(),
        };
        notes.push(format!("{} {why}.", source_name(&s.name)));
    }
    match r.errors.len() {
        0 => {}
        1 => notes.push(format!("1 plist couldn't be read: {}", r.errors[0].path)),
        n => notes.push(format!(
            "{n} plists couldn't be read, such as {}",
            r.errors[0].path
        )),
    }
    Ok((r.items.into_iter().map(ItemJson::item).collect(), notes))
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct JobJson {
    label: String,
    domain: String,
    pid: i64,
    disabled: bool,
    keep_alive: bool,
    run_at_load: bool,
    program: String,
    program_arguments: Vec<String>,
    plist_path: String,
    start_interval: i64,
    start_calendar: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct ServicesJson {
    jobs: Vec<JobJson>,
}

/// Reads `spectra services --json`. Launches of apps the person opened (`application.…`) are
/// left out: they're the apps themselves, not services.
pub fn parse_services(json: &str) -> Result<Vec<Item>, String> {
    let r: ServicesJson = serde_json::from_str(json)
        .map_err(|e| format!("Spectra's services list didn't read as JSON: {e}"))?;
    Ok(r.jobs
        .into_iter()
        .filter(|j| !j.label.is_empty() && !j.label.starts_with("application."))
        .map(|j| {
            let system = j.domain == "system";
            let mut triggers = Vec::new();
            if j.keep_alive {
                triggers.push(Trigger::KeepAlive);
            }
            if j.run_at_load {
                triggers.push(if system {
                    Trigger::Boot
                } else {
                    Trigger::Login
                });
            }
            if j.start_interval > 0 {
                triggers.push(Trigger::Interval);
            }
            if !j.start_calendar.is_empty() {
                triggers.push(Trigger::Calendar);
            }
            if triggers.is_empty() {
                triggers.push(Trigger::OnDemand);
            }
            let pid = u32::try_from(j.pid).ok().filter(|p| *p > 0);
            Item {
                apple: j.label.starts_with("com.apple.") || j.plist_path.starts_with("/System/"),
                name: String::new(),
                kind: Kind::Service,
                scope: if system { Scope::System } else { Scope::User },
                program: if j.program.is_empty() {
                    j.program_arguments.first().cloned().unwrap_or_default()
                } else {
                    j.program
                },
                plist_path: j.plist_path,
                triggers,
                disabled: j.disabled,
                loaded: true,
                running: pid.is_some(),
                pid,
                owner: None,
                duplicate: false,
                label: j.label,
            }
        })
        .collect())
}

/// The startup items, then the services that aren't one of them. A service whose label is an
/// owning app's bundle id, or under it, joins that app.
pub fn merge(mut items: Vec<Item>, services: Vec<Item>) -> Vec<Item> {
    let owners: Vec<Owner> = {
        let mut o: Vec<Owner> = items
            .iter()
            .filter_map(|i| i.owner.clone())
            .filter(|o| !o.bundle_id.is_empty())
            .collect();
        o.sort_by(|a, b| {
            b.bundle_id
                .len()
                .cmp(&a.bundle_id.len())
                .then_with(|| a.bundle_id.cmp(&b.bundle_id))
        });
        o.dedup_by(|a, b| a.bundle_id == b.bundle_id);
        o
    };
    let known: std::collections::HashSet<String> = items.iter().map(|i| i.label.clone()).collect();
    for mut s in services {
        if known.contains(&s.label) {
            continue;
        }
        s.owner = owners
            .iter()
            .find(|o| s.label == o.bundle_id || s.label.starts_with(&format!("{}.", o.bundle_id)))
            .cloned();
        items.push(s);
    }
    items
}

/// What the filters keep, in the order they're drawn.
pub fn shown<'a>(
    items: &'a [Item],
    query: &str,
    running_only: bool,
    not_apple: bool,
) -> Vec<&'a Item> {
    let q = query.trim().to_lowercase();
    items
        .iter()
        .filter(|i| !(not_apple && i.apple) && (!running_only || i.is_running()) && i.matches(&q))
        .collect()
}

/// Which heading a group sits under.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Heading {
    /// An app: its login item, agents, daemons and helpers together.
    App(Owner),
    /// Startup items no app owns.
    Unowned,
    /// Background services no app owns.
    Services,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Group<'a> {
    pub heading: Heading,
    pub items: Vec<&'a Item>,
}

impl Group<'_> {
    pub fn title(&self) -> String {
        match &self.heading {
            Heading::App(o) => o.name().to_owned(),
            Heading::Unowned => "Not from an app".into(),
            Heading::Services => "Other background services".into(),
        }
    }

    /// Stable, and free of `/`, which element keys use.
    pub fn id(&self) -> String {
        match &self.heading {
            Heading::App(o) => format!("app-{}", o.key().replace('/', "_")),
            Heading::Unowned => "unowned".into(),
            Heading::Services => "services".into(),
        }
    }
}

fn caseless(a: &str, b: &str) -> Ordering {
    a.to_lowercase()
        .cmp(&b.to_lowercase())
        .then_with(|| a.cmp(b))
}

/// Groups by owning app (A to Z), then unowned startup items, then unowned services. Inside a
/// group: login items, agents, daemons, services, each A to Z.
pub fn group<'a>(items: &[&'a Item]) -> Vec<Group<'a>> {
    let mut apps: Vec<Group<'a>> = Vec::new();
    let (mut unowned, mut services) = (Vec::new(), Vec::new());
    for &i in items {
        match &i.owner {
            Some(o) => match apps
                .iter_mut()
                .find(|g| matches!(&g.heading, Heading::App(h) if h.key() == o.key()))
            {
                Some(g) => g.items.push(i),
                None => apps.push(Group {
                    heading: Heading::App(o.clone()),
                    items: vec![i],
                }),
            },
            None if i.kind == Kind::Service => services.push(i),
            None => unowned.push(i),
        }
    }
    apps.sort_by(|a, b| caseless(&a.title(), &b.title()).then_with(|| a.id().cmp(&b.id())));
    let mut out = apps;
    if !unowned.is_empty() {
        out.push(Group {
            heading: Heading::Unowned,
            items: unowned,
        });
    }
    if !services.is_empty() {
        out.push(Group {
            heading: Heading::Services,
            items: services,
        });
    }
    for g in &mut out {
        g.items.sort_by(|a, b| {
            a.kind
                .cmp(&b.kind)
                .then_with(|| caseless(a.display_name(), b.display_name()))
                .then_with(|| a.label.cmp(&b.label))
        });
    }
    out
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Action {
    Disable,
    Enable,
}

impl Action {
    pub fn verb(self) -> &'static str {
        match self {
            Action::Disable => "Disable",
            Action::Enable => "Enable",
        }
    }

    fn arg(self) -> &'static str {
        match self {
            Action::Disable => "disable",
            Action::Enable => "enable",
        }
    }

    /// The command for `label`: user scope, always. The flags end with `--`, so a label that
    /// starts with `-` is never read as one.
    pub fn args(self, label: &str) -> Vec<String> {
        vec![
            "startup".into(),
            self.arg().into(),
            "--json".into(),
            "--scope".into(),
            "user".into(),
            "--".into(),
            label.into(),
        ]
    }
}

/// A change waiting for the person to confirm it: the item as it was drawn.
#[derive(Clone, Debug, PartialEq)]
pub struct Ask {
    pub item: Item,
    pub action: Action,
}

impl Ask {
    /// What will change, in lines: the first plain, the rest worth a second look.
    pub fn consequence(&self) -> Vec<String> {
        let name = self.item.display_name();
        let label = &self.item.label;
        match self.action {
            Action::Disable => {
                let mut lines = vec![format!(
                    "{name} ({label}) won't start again, at your next login or on its own, until you enable it. You can enable it again here."
                )];
                if self.item.is_running() {
                    let pid = self
                        .item
                        .pid
                        .map(|p| format!(" (pid {p})"))
                        .unwrap_or_default();
                    lines.push(format!(
                        "It's running now{pid}. Disabling also unloads it, which stops it."
                    ));
                } else if self.item.loaded {
                    lines.push("It's loaded. Disabling also unloads it.".into());
                }
                lines
            }
            Action::Enable => {
                let now = if self.item.loaded { "" } else { " and starts now" };
                vec![format!(
                    "{name} ({label}) can start again. It will start at your next login{now}."
                )]
            }
        }
    }
}

/// Where a confirmed change stands.
#[derive(Clone, Debug, PartialEq)]
pub enum ChangeStatus {
    Sending,
    Done(String),
    /// Spectra ran nothing, and says why.
    Refused(String),
    Failed(String),
}

#[derive(Clone, Debug, PartialEq)]
pub struct ChangeOutcome {
    pub name: String,
    pub label: String,
    pub action: Action,
    pub status: ChangeStatus,
}

impl ChangeOutcome {
    pub fn line(&self) -> String {
        match &self.status {
            ChangeStatus::Sending => format!(
                "{}ing {}…",
                self.action.verb().trim_end_matches('e'),
                self.name
            ),
            ChangeStatus::Done(s) | ChangeStatus::Refused(s) | ChangeStatus::Failed(s) => s.clone(),
        }
    }
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct CommandJson {
    argv: Vec<String>,
    exit_code: i32,
    stderr: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct ChangeJson {
    action: String,
    refused: bool,
    reason: String,
    message: String,
    pending: bool,
    mismatch: String,
    commands: Vec<CommandJson>,
    after: Option<ItemJson>,
}

/// What a change came to, from Spectra's JSON (or, without any, from how it exited).
pub fn parse_change(action: Action, name: &str, ran: &Ran) -> ChangeStatus {
    let verb = action.arg();
    let parsed = serde_json::from_str::<ChangeJson>(ran.stdout.trim())
        .ok()
        .filter(|c| !c.action.is_empty() || c.refused);
    let Some(c) = parsed else {
        return ChangeStatus::Failed(match (ran.said(), ran.code) {
            (Some(line), _) => format!("Couldn't {verb} {name}: {line}"),
            (None, Some(code)) => format!(
                "Couldn't {verb} {name}: spectra startup {verb} stopped with exit status {code}"
            ),
            (None, None) => format!("Couldn't {verb} {name}: spectra startup {verb} was stopped"),
        });
    };
    if c.refused {
        // "refusing to disable <label>: why" — the sentence around it already says that much.
        let message = match c
            .message
            .strip_prefix("refusing to ")
            .and_then(|m| m.split_once(": "))
        {
            Some((_, why)) => why.to_owned(),
            None => c.message,
        };
        let why = if message.is_empty() {
            c.reason.replace('_', " ")
        } else {
            message
        };
        return ChangeStatus::Refused(format!("Spectra didn't {verb} {name}: {why}"));
    }
    if !c.mismatch.is_empty() {
        return ChangeStatus::Failed(format!("{name} didn't change: {}", c.mismatch));
    }
    if ran.code != Some(0) {
        let failed = c.commands.iter().find(|cmd| cmd.exit_code != 0);
        let why = failed
            .map(|cmd| {
                let what = cmd
                    .argv
                    .iter()
                    .take(2)
                    .cloned()
                    .collect::<Vec<_>>()
                    .join(" ");
                match cmd.stderr.lines().map(str::trim).find(|l| !l.is_empty()) {
                    Some(line) => format!("{what} failed: {line}"),
                    None => format!("{what} exited with status {}", cmd.exit_code),
                }
            })
            .or_else(|| ran.said().map(str::to_owned))
            .unwrap_or_else(|| {
                format!(
                    "spectra startup {verb} exited with status {}",
                    ran.code.unwrap_or(-1)
                )
            });
        return ChangeStatus::Failed(format!("Couldn't {verb} {name}: {why}"));
    }
    let after = c.after.map(ItemJson::item);
    ChangeStatus::Done(match action {
        Action::Disable => {
            let mut s = format!("{name} is disabled. It won't start again until you enable it.");
            if c.pending {
                s.push_str(" launchd is still stopping it.");
            } else if after.as_ref().is_some_and(|a| !a.is_running()) {
                s.push_str(" It isn't running.");
            }
            s
        }
        Action::Enable => {
            let mut s = format!("{name} is enabled.");
            if let Some(a) = &after {
                s.push_str(if a.is_running() {
                    " It's running."
                } else {
                    " It's loaded and starts when launchd next starts it."
                });
            }
            s
        }
    })
}

/// What a worker hands back.
pub enum Landed {
    Read {
        job: u64,
        result: Result<Listing, String>,
    },
    Changed {
        job: u64,
        status: ChangeStatus,
    },
}

/// The Startup page's state.
pub struct Startup {
    pub runner: Arc<dyn Runner>,
    /// Off in tests: work runs on the calling thread and lands on the next `land_startup`.
    pub threaded: bool,
    pub listing: Option<Listing>,
    /// A listing is being read.
    pub reading: bool,
    /// Why the last read failed; the listing before it stays.
    pub error: Option<String>,
    pub query: TextEdit,
    pub running_only: bool,
    /// Hides Apple's items (and reads without `--include-apple`).
    pub not_apple: bool,
    pub confirm: Option<Ask>,
    pub outcome: Option<ChangeOutcome>,
    /// A change has landed and the re-read that shows its effect hasn't: the rows still show the
    /// state from before it.
    settling: bool,
    read_job: u64,
    change_job: u64,
    tx: Sender<Landed>,
    rx: Receiver<Landed>,
}

impl Startup {
    pub fn new() -> Startup {
        let (tx, rx) = channel();
        Startup {
            runner: Arc::new(SpectraBin),
            threaded: true,
            listing: None,
            reading: false,
            error: None,
            query: TextEdit::new(""),
            running_only: false,
            not_apple: true,
            confirm: None,
            outcome: None,
            settling: false,
            read_job: 0,
            change_job: 0,
            tx,
            rx,
        }
    }

    /// Whether a confirmed change is still on its way.
    pub fn sending(&self) -> bool {
        self.outcome
            .as_ref()
            .is_some_and(|o| o.status == ChangeStatus::Sending)
    }

    /// Whether a change is on its way or its effect isn't on screen yet: no other can start.
    pub fn busy(&self) -> bool {
        self.sending() || self.settling
    }

    /// The listing's items the filters keep.
    pub fn shown(&self) -> Vec<&Item> {
        self.listing
            .as_ref()
            .map(|l| {
                shown(
                    &l.items,
                    &self.query.text,
                    self.running_only,
                    self.not_apple,
                )
            })
            .unwrap_or_default()
    }

    /// The changeable item a row stands for: its label and the plist it came from, so two
    /// rows sharing a label can't act on each other.
    pub fn item(&self, label: &str, plist_path: &str) -> Option<&Item> {
        self.listing
            .as_ref()?
            .items
            .iter()
            .find(|i| i.label == label && i.plist_path == plist_path && i.kind != Kind::Service)
    }
}

impl Default for Startup {
    fn default() -> Startup {
        Startup::new()
    }
}

/// Reads both commands: a failed startup list fails the read; a failed services list is a note.
fn read(runner: &dyn Runner, apple: bool) -> Result<Listing, String> {
    let mut args: Vec<String> = ["startup", "list", "--json", "--scope", "all"]
        .map(String::from)
        .to_vec();
    if apple {
        args.push("--include-apple".into());
    }
    let ran = runner.run(&args, LIST_DEADLINE)?;
    if ran.code != Some(0) || ran.stdout.trim().is_empty() {
        return Err(list_failure(&ran));
    }
    let (items, mut notes) = parse_startup(&ran.stdout)?;
    let services_args: Vec<String> = ["services", "--json", "--domain", "all"]
        .map(String::from)
        .to_vec();
    let services = match runner.run(&services_args, LIST_DEADLINE) {
        Ok(r) if r.code == Some(0) => parse_services(&r.stdout),
        Ok(r) => Err(r.said().map_or_else(
            || format!("exit status {}", r.code.unwrap_or(-1)),
            str::to_owned,
        )),
        Err(e) => Err(e),
    };
    let services = services.unwrap_or_else(|why| {
        notes.push(format!("Background services couldn't be read: {why}"));
        Vec::new()
    });
    Ok(Listing {
        items: merge(items, services),
        notes,
        apple,
    })
}

/// Why a startup list printed nothing to read.
fn list_failure(ran: &Ran) -> String {
    let said = ran.said().unwrap_or("");
    let lower = said.to_ascii_lowercase();
    if lower.contains("unknown command") || lower.contains("stat startup") {
        return "This Spectra has no `spectra startup`. Update Spectra to see what starts at login.".into();
    }
    if lower.contains("unsupported") || lower.contains("not supported") {
        return "Spectra can't list startup items on this system.".into();
    }
    match (said.is_empty(), ran.code) {
        (false, _) => said.to_owned(),
        (true, Some(c)) => format!("spectra startup list stopped with exit status {c}"),
        (true, None) => "spectra startup list was stopped".into(),
    }
}

/// Runs `work` on a thread of its own (or here, in tests), turning a panic into `failed`, and
/// sends what it came to.
fn submit(
    st: &Startup,
    wake: Option<rune::Waker>,
    work: impl FnOnce() -> Landed + Send + 'static,
    failed: Landed,
) {
    let tx = st.tx.clone();
    if !st.threaded {
        let _ = tx.send(work());
        return;
    }
    // Taken once: by the thread if the work panics, or here if the thread never starts.
    let failed = Arc::new(Mutex::new(Some(failed)));
    let take = |f: &Mutex<Option<Landed>>| f.lock().ok().and_then(|mut f| f.take());
    let (on_panic, failed_tx, wake2) = (Arc::clone(&failed), tx.clone(), wake.clone());
    let job = move || {
        let landed = std::panic::catch_unwind(std::panic::AssertUnwindSafe(work));
        if let Some(l) = landed.ok().or_else(|| take(&on_panic)) {
            let _ = tx.send(l);
        }
        if let Some(wake) = wake {
            wake();
        }
    };
    if std::thread::Builder::new()
        .name("spectra-startup".into())
        .spawn(job)
        .is_err()
    {
        if let Some(l) = take(&failed) {
            let _ = failed_tx.send(l);
        }
        if let Some(wake) = wake2 {
            wake();
        }
    }
}

impl App {
    /// Reads what starts at login, unless a read is already on its way.
    pub fn read_startup(&mut self) {
        let st = &mut self.startup;
        if st.reading {
            return;
        }
        st.reading = true;
        st.read_job += 1;
        let job = st.read_job;
        let apple = !st.not_apple;
        let runner = Arc::clone(&st.runner);
        let failed = Landed::Read {
            job,
            result: Err(
                "Couldn't ask Spectra for startup items: the reading thread failed. Try again."
                    .into(),
            ),
        };
        submit(
            st,
            self.wake.clone(),
            move || Landed::Read {
                job,
                result: read(runner.as_ref(), apple),
            },
            failed,
        );
    }

    /// Lands what the Startup workers finished. True when anything landed.
    pub fn land_startup(&mut self) -> bool {
        let mut landed = false;
        while let Ok(l) = self.startup.rx.try_recv() {
            landed = true;
            let st = &mut self.startup;
            match l {
                Landed::Read { job, result } if job == st.read_job => {
                    st.reading = false;
                    st.settling = false;
                    match result {
                        Ok(listing) => {
                            st.listing = Some(listing);
                            st.error = None;
                        }
                        Err(why) => st.error = Some(why),
                    }
                }
                Landed::Read { .. } => {}
                Landed::Changed { job, status } => {
                    if job != st.change_job {
                        continue;
                    }
                    let Some(o) = st
                        .outcome
                        .as_mut()
                        .filter(|o| o.status == ChangeStatus::Sending)
                    else {
                        continue;
                    };
                    let changed = matches!(status, ChangeStatus::Done(_) | ChangeStatus::Failed(_));
                    o.status = status;
                    // Whatever launchd did, the list should show it.
                    if changed {
                        st.reading = false;
                        st.settling = true;
                        self.read_startup();
                    }
                }
            }
        }
        landed
    }

    /// "Not from Apple": hiding is a filter; showing reads Apple's items once.
    pub fn startup_toggle_apple(&mut self) {
        self.startup.not_apple = !self.startup.not_apple;
        let have_apple = self.startup.listing.as_ref().is_some_and(|l| l.apple);
        if !self.startup.not_apple && !have_apple {
            self.startup.reading = false;
            self.read_startup();
        }
    }

    pub fn startup_toggle_running(&mut self) {
        self.startup.running_only = !self.startup.running_only;
    }

    /// Opens the confirmation for changing `label`, as it is in the listing. It never changes
    /// anything itself, and opens nothing for an item that can't be changed or while another
    /// change is open or on its way.
    pub fn ask_startup(&mut self, label: &str, plist_path: &str, action: Action) {
        let st = &mut self.startup;
        if st.confirm.is_some() || st.busy() {
            return;
        }
        let Some(item) = st
            .item(label, plist_path)
            .filter(|i| i.change_refusal().is_none())
            .cloned()
        else {
            return;
        };
        st.confirm = Some(Ask { item, action });
    }

    pub fn cancel_startup(&mut self) {
        self.startup.confirm = None;
    }

    /// The confirmation's button: sends the one change. The confirmation is used up on the
    /// first press, so a second sends nothing.
    pub fn confirm_startup(&mut self) {
        let st = &mut self.startup;
        let Some(Ask { item, action }) = st.confirm.take() else {
            return;
        };
        st.change_job += 1;
        let job = st.change_job;
        let name = item.display_name().to_owned();
        st.outcome = Some(ChangeOutcome {
            name: name.clone(),
            label: item.label.clone(),
            action,
            status: ChangeStatus::Sending,
        });
        let runner = Arc::clone(&st.runner);
        let args = action.args(&item.label);
        let failed = Landed::Changed {
            job,
            status: ChangeStatus::Failed(format!(
                "Spectra hit an internal error while trying to {} {name}. Refresh to see where it stands.",
                action.arg()
            )),
        };
        submit(
            st,
            self.wake.clone(),
            move || {
                let status = match runner.run(&args, CHANGE_DEADLINE) {
                    Ok(ran) => parse_change(action, &name, &ran),
                    Err(why) => {
                        ChangeStatus::Failed(format!("Couldn't {} {name}: {why}", action.arg()))
                    }
                };
                Landed::Changed { job, status }
            },
            failed,
        );
    }

    /// Dismisses the change's outcome, except while it's on its way.
    pub fn dismiss_startup_outcome(&mut self) {
        if !self.startup.busy() {
            self.startup.outcome = None;
        }
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    use crate::sample_startup::{LIST, SERVICES};

    /// Records every command and answers from a script: list and services from the fixtures,
    /// a change with `change`.
    pub struct Fake {
        pub calls: Mutex<Vec<Vec<String>>>,
        pub list: Result<Ran, String>,
        pub services: Result<Ran, String>,
        pub change: Result<Ran, String>,
        /// A list waits for this lock: a test holds it to keep a re-read in flight.
        pub gate: Arc<Mutex<()>>,
    }

    pub fn ok(stdout: &str) -> Result<Ran, String> {
        Ok(Ran {
            code: Some(0),
            stdout: stdout.into(),
            stderr: String::new(),
        })
    }

    impl Fake {
        pub fn new() -> Fake {
            Fake {
                calls: Mutex::new(Vec::new()),
                list: ok(LIST),
                services: ok(SERVICES),
                change: ok(crate::sample_startup::DISABLED_JSON),
                gate: Arc::new(Mutex::new(())),
            }
        }

        pub fn calls(&self) -> Vec<Vec<String>> {
            self.calls.lock().unwrap().clone()
        }

        /// The changes sent: every `startup disable|enable`.
        pub fn changes(&self) -> Vec<Vec<String>> {
            self.calls()
                .into_iter()
                .filter(|c| c.len() > 1 && c[0] == "startup" && c[1] != "list")
                .collect()
        }
    }

    impl Runner for Fake {
        fn run(&self, args: &[String], _: Duration) -> Result<Ran, String> {
            self.calls.lock().unwrap().push(args.to_vec());
            match (args[0].as_str(), args.get(1).map(String::as_str)) {
                ("startup", Some("list")) => {
                    drop(self.gate.lock());
                    self.list.clone()
                }
                ("services", _) => self.services.clone(),
                _ => self.change.clone(),
            }
        }
    }

    fn listing() -> Vec<Item> {
        let (items, _) = parse_startup(LIST).unwrap();
        merge(items, parse_services(SERVICES).unwrap())
    }

    fn by<'a>(items: &'a [Item], label: &str) -> &'a Item {
        items
            .iter()
            .find(|i| i.label == label)
            .unwrap_or_else(|| panic!("no {label}"))
    }

    #[test]
    fn a_real_startup_list_reads_every_kind_and_scope() {
        let (items, notes) = parse_startup(LIST).unwrap();
        assert_eq!(items.len(), 16);
        let helper = by(&items, "2BUA8C4S2C.com.1password.browser-helper");
        assert_eq!(helper.kind, Kind::LoginItem);
        assert_eq!(helper.scope, Scope::User);
        assert_eq!(helper.pid, Some(1107));
        assert!(helper.running);
        assert_eq!(helper.owner.as_ref().unwrap().app, "1Password");
        let vmnetd = by(&items, "com.docker.vmnetd");
        assert_eq!(
            (vmnetd.kind, vmnetd.scope),
            (Kind::LaunchDaemon, Scope::System)
        );
        assert_eq!(vmnetd.triggers, vec![Trigger::Boot]);
        let brew = by(&items, "com.github.domt4.homebrew-autoupdate");
        assert_eq!(brew.triggers, vec![Trigger::Login, Trigger::Interval]);
        assert_eq!(brew.trigger_text(), "At login · On an interval");
        assert!(brew.owner.is_none());
        assert!(!brew.plist_path.is_empty());
        assert!(
            items.iter().all(|i| !i.apple),
            "read without --include-apple"
        );
        assert_eq!(
            notes.len(),
            1,
            "two keystone plists without a Label: {notes:?}"
        );
        assert!(notes[0].starts_with("2 plists couldn't be read"));
    }

    #[test]
    fn a_real_services_list_reads_triggers_and_leaves_out_app_launches() {
        let services = parse_services(SERVICES).unwrap();
        assert!(
            services
                .iter()
                .all(|s| !s.label.starts_with("application."))
        );
        assert!(services.iter().all(|s| s.kind == Kind::Service));
        let sshd = by(&services, "com.openssh.sshd");
        assert!(sshd.apple, "its plist is under /System");
        assert!(sshd.disabled);
        assert_eq!(sshd.program, "/usr/libexec/sshd-keygen-wrapper");
        let watchman = by(&services, "com.github.facebook.watchman");
        assert_eq!(watchman.triggers, vec![Trigger::KeepAlive, Trigger::Login]);
        assert_eq!(watchman.pid, Some(1114));
        assert!(by(&services, "com.apple.Finder").apple);
    }

    #[test]
    fn missing_fields_never_fail_a_read() {
        let (items, notes) = parse_startup(r#"{"items":[{"label":"x"}]}"#).unwrap();
        assert_eq!(items[0].display_name(), "x");
        assert_eq!(items[0].kind, Kind::LaunchAgent);
        assert_eq!(items[0].trigger_text(), "On demand");
        assert!(notes.is_empty());
        assert!(
            parse_services(r#"{"jobs":[{"label":"y"}]}"#).unwrap()[0]
                .pid
                .is_none()
        );
        assert!(parse_startup("not json").is_err());
    }

    #[test]
    fn a_source_needing_root_is_a_note() {
        let (_, notes) = parse_startup(r#"{"items":[],"sources":[{"name":"btm","status":"needs_root"},{"name":"launchd_plist","status":"ok"}]}"#).unwrap();
        assert_eq!(
            notes,
            vec!["Login Items need an administrator to read.".to_owned()]
        );
    }

    #[test]
    fn services_already_listed_are_merged_and_the_rest_join_their_app() {
        let items = listing();
        let n = items
            .iter()
            .filter(|i| i.label == "com.github.facebook.watchman")
            .count();
        assert_eq!(n, 1, "a service that is a startup item is listed once");
        assert_eq!(
            by(&items, "com.github.facebook.watchman").kind,
            Kind::LaunchAgent
        );
        let ipc = by(&items, "com.valvesoftware.steam.ipctool");
        assert_eq!(ipc.kind, Kind::Service);
        assert_eq!(
            ipc.owner.as_ref().map(|o| o.app.as_str()),
            Some("Steam"),
            "under Steam's bundle id"
        );
        assert!(
            by(&items, "com.anthropic.claudefordesktop.ShipIt")
                .owner
                .is_none()
        );
    }

    #[test]
    fn groups_are_apps_a_to_z_then_unowned_items_then_services() {
        let items = listing();
        let kept = shown(&items, "", false, true);
        let groups = group(&kept);
        let titles: Vec<String> = groups.iter().map(Group::title).collect();
        let apps = &titles[..titles.len() - 2];
        let mut sorted = apps.to_vec();
        sorted.sort_by_key(|t| t.to_lowercase());
        assert_eq!(apps, sorted.as_slice());
        assert_eq!(
            &titles[titles.len() - 2..],
            ["Not from an app", "Other background services"]
        );
        let docker = groups.iter().find(|g| g.title() == "Docker").unwrap();
        let kinds: Vec<Kind> = docker.items.iter().map(|i| i.kind).collect();
        assert_eq!(
            kinds,
            vec![Kind::LoginItem, Kind::LaunchDaemon],
            "its login item and its daemon together"
        );
        let steam = groups.iter().find(|g| g.title() == "Steam").unwrap();
        assert_eq!(
            steam.items.len(),
            3,
            "login item, agent and the ipc service"
        );
        assert!(groups.iter().all(|g| !g.id().contains('/')));
    }

    #[test]
    fn the_filters_keep_running_items_hide_apple_and_search() {
        let items = listing();
        let all = shown(&items, "", false, false);
        let no_apple = shown(&items, "", false, true);
        assert!(all.iter().any(|i| i.label == "com.apple.Finder"));
        assert!(no_apple.iter().all(|i| !i.apple));
        assert!(no_apple.len() < all.len());
        let running = shown(&items, "", true, true);
        assert!(!running.is_empty() && running.iter().all(|i| i.is_running()));
        assert!(running.iter().any(|i| i.label == "com.docker.vmnetd"));
        let q = shown(&items, "  DOCKER ", false, true);
        assert!(
            !q.is_empty()
                && q.iter().all(|i| i.label.contains("docker")
                    || i.owner.as_ref().is_some_and(|o| o.app == "Docker"))
        );
        assert_eq!(shown(&items, "1114", false, true).len(), 1, "by pid");
    }

    #[test]
    fn only_the_persons_own_launch_agents_can_change() {
        let items = listing();
        assert_eq!(
            by(&items, "com.github.domt4.homebrew-autoupdate").change_refusal(),
            None
        );
        assert_eq!(by(&items, "com.jetbrains.toolbox").change_refusal(), None);
        assert_eq!(
            by(&items, "com.docker.vmnetd").change_refusal(),
            Some("Needs an administrator; Spectra Desktop doesn't run sudo")
        );
        assert_eq!(
            by(&items, "us.zoom.updater").change_refusal(),
            Some("Needs an administrator; Spectra Desktop doesn't run sudo")
        );
        assert!(
            by(&items, "com.docker.helper")
                .change_refusal()
                .unwrap()
                .contains("Login Items")
        );
        assert!(
            by(&items, "com.valvesoftware.steam.ipctool")
                .change_refusal()
                .is_some()
        );
        let mut apple = by(&items, "com.jetbrains.toolbox").clone();
        apple.label = "com.apple.example".into();
        assert!(apple.change_refusal().unwrap().contains("Apple"));
        let mut dup = by(&items, "com.jetbrains.toolbox").clone();
        dup.duplicate = true;
        assert!(dup.change_refusal().is_some());
    }

    #[test]
    fn a_change_reads_as_spectra_said() {
        let ran = |code, stdout: &str, stderr: &str| Ran {
            code: Some(code),
            stdout: stdout.into(),
            stderr: stderr.into(),
        };
        let done = parse_change(
            Action::Disable,
            "brew",
            &ran(0, crate::sample_startup::DISABLED_JSON, ""),
        );
        assert_eq!(
            done,
            ChangeStatus::Done(
                "brew is disabled. It won't start again until you enable it. It isn't running."
                    .into()
            )
        );
        let refused = parse_change(
            Action::Disable,
            "brew",
            &ran(3, crate::sample_startup::REFUSED_JSON, ""),
        );
        assert!(
            matches!(&refused, ChangeStatus::Refused(s) if s.starts_with("Spectra didn't disable brew: the label is defined by 2 plists")),
            "{refused:?}"
        );
        let mismatch = r#"{"action":"disable","label":"x","refused":false,"commands":[],"mismatch":"still enabled"}"#;
        assert_eq!(
            parse_change(Action::Disable, "x", &ran(1, mismatch, "")),
            ChangeStatus::Failed("x didn't change: still enabled".into())
        );
        let failed = r#"{"action":"disable","label":"x","refused":false,"commands":[{"argv":["launchctl","disable","gui/501/x"],"exit_code":5,"stderr":"Input/output error"}]}"#;
        assert_eq!(
            parse_change(Action::Disable, "x", &ran(1, failed, "")),
            ChangeStatus::Failed(
                "Couldn't disable x: launchctl disable failed: Input/output error".into()
            )
        );
        assert_eq!(
            parse_change(Action::Enable, "x", &ran(2, "", "usage: spectra startup")),
            ChangeStatus::Failed("Couldn't enable x: usage: spectra startup".into())
        );
    }

    #[test]
    fn a_failed_list_says_why() {
        let fake = Fake {
            list: Ok(Ran {
                code: Some(1),
                stdout: String::new(),
                stderr: "startup: launchctl print: exit status 5\n".into(),
            }),
            ..Fake::new()
        };
        assert_eq!(
            read(&fake, false).unwrap_err(),
            "startup: launchctl print: exit status 5"
        );
        let old = Fake {
            list: Ok(Ran {
                code: Some(1),
                stdout: String::new(),
                stderr: "stat startup: no such file or directory".into(),
            }),
            ..Fake::new()
        };
        assert!(
            read(&old, false)
                .unwrap_err()
                .contains("no `spectra startup`")
        );
        let spawn = Fake {
            list: Err(
                "Couldn't run spectra: not found. Install Spectra or set SPECTRA_BIN.".into(),
            ),
            ..Fake::new()
        };
        assert!(
            read(&spawn, false)
                .unwrap_err()
                .starts_with("Couldn't run spectra")
        );
    }

    #[test]
    fn a_failed_services_list_keeps_the_startup_items_and_says_so() {
        let fake = Fake {
            services: Err("spectra took longer than 60 s and was stopped".into()),
            ..Fake::new()
        };
        let l = read(&fake, false).unwrap();
        assert_eq!(l.items.len(), 16);
        assert!(
            l.notes
                .iter()
                .any(|n| n.starts_with("Background services couldn't be read"))
        );
    }

    #[test]
    fn the_read_asks_for_apple_only_when_shown() {
        let fake = Fake::new();
        read(&fake, false).unwrap();
        read(&fake, true).unwrap();
        let calls = fake.calls();
        assert_eq!(calls[0], ["startup", "list", "--json", "--scope", "all"]);
        assert_eq!(calls[1], ["services", "--json", "--domain", "all"]);
        assert_eq!(calls[2].last().map(String::as_str), Some("--include-apple"));
    }

    #[test]
    fn a_change_puts_its_flags_before_the_label_and_ends_them() {
        for label in ["com.example.x", "--", "-x", "--json"] {
            assert_eq!(
                Action::Disable.args(label),
                ["startup", "disable", "--json", "--scope", "user", "--", label]
            );
            assert_eq!(
                Action::Enable.args(label),
                ["startup", "enable", "--json", "--scope", "user", "--", label]
            );
        }
    }

    #[test]
    fn quit_stops_a_read_but_not_a_change() {
        let args = |a: &[&str]| a.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        let quit = &CANCEL;
        assert!(std::ptr::eq(
            cancel_for(&args(&["startup", "list", "--json"])),
            quit
        ));
        assert!(std::ptr::eq(cancel_for(&args(&["services", "--json"])), quit));
        for action in [Action::Disable, Action::Enable] {
            let flag = cancel_for(&action.args("-x"));
            assert!(!std::ptr::eq(flag, quit), "a change isn't killed at quit");
            assert!(!flag.load(std::sync::atomic::Ordering::SeqCst));
        }
        assert!(CHANGE_DEADLINE > Duration::from_secs(120));
    }

    #[test]
    fn the_confirmation_says_when_it_starts_and_what_unloads() {
        let mut item = by(&listing(), "com.jetbrains.toolbox").clone();
        let ask = |item: &Item, action| Ask { item: item.clone(), action }.consequence();
        item.loaded = false;
        item.running = false;
        item.pid = None;
        assert_eq!(ask(&item, Action::Enable).len(), 1);
        assert!(ask(&item, Action::Enable)[0].ends_with("It will start at your next login and starts now."));
        item.loaded = true;
        assert!(ask(&item, Action::Enable)[0].ends_with("It will start at your next login."));
        assert!(ask(&item, Action::Disable).iter().any(|l| l.contains("loaded") && l.contains("unloads")));
        item.loaded = false;
        assert_eq!(ask(&item, Action::Disable).len(), 1);
    }
}
