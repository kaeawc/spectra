//! The host's local time zone, chosen the way Go's `time` package chooses `time.Local` on Unix
//! (`initLocal` in `time/zoneinfo_unix.go`), but read through [`Env`] and [`Fs`] so tests and
//! recorded cases pick their own.
//!
//! - `TZ` unset: `/etc/localtime`.
//! - `TZ=""` or `TZ=UTC`: UTC.
//! - `TZ=:name` is `TZ=name`; an absolute path names a TZif file; any other name is looked up
//!   in `/usr/share/zoneinfo/`, `/usr/share/lib/zoneinfo/`, `/usr/lib/locale/TZ/` and
//!   `/etc/zoneinfo`.
//! - Anything that cannot be read or parsed: UTC.
//!
//! Go's last-resort `$GOROOT/lib/time/zoneinfo.zip` and embedded tzdata are not consulted.

use std::path::Path;

use jiff::tz::TimeZone;
use spectra_base::env::Env;
use spectra_base::fs::Fs;

/// Go's `platformZoneSources`.
const ZONE_SOURCES: &[&str] = &["/usr/share/zoneinfo/", "/usr/share/lib/zoneinfo/", "/usr/lib/locale/TZ/", "/etc/zoneinfo"];

/// Go's `time.Local` for this environment and filesystem.
pub fn local(env: &dyn Env, fs: &dyn Fs) -> TimeZone {
    let Some(tz) = env.var("TZ") else {
        return load(fs, "localtime", &["/etc"]).unwrap_or(TimeZone::UTC);
    };
    let tz = tz.strip_prefix(':').unwrap_or(&tz);
    if tz.is_empty() || tz == "UTC" {
        return TimeZone::UTC;
    }
    let found = if tz.starts_with('/') { load(fs, tz, &[""]) } else { load(fs, tz, ZONE_SOURCES) };
    found.unwrap_or(TimeZone::UTC)
}

/// Go's `loadLocation`: the first source holding a parseable TZif file for `name`.
fn load(fs: &dyn Fs, name: &str, sources: &[&str]) -> Option<TimeZone> {
    sources.iter().find_map(|source| {
        let path = if source.is_empty() { name.to_string() } else { format!("{source}/{name}") };
        let data = fs.read(Path::new(&path)).ok()?;
        TimeZone::tzif(name, &data).ok()
    })
}

#[cfg(test)]
mod tests;
