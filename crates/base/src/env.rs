//! Environment variables, behind a trait so tests set them without touching the process.

use std::collections::BTreeMap;

/// Reads environment variables. Collectors take `&dyn Env` instead of calling `std::env::var`.
pub trait Env: Send + Sync {
    /// The variable's value, or `None` when it is unset or not valid UTF-8.
    fn var(&self, key: &str) -> Option<String>;
    /// Every variable that is valid UTF-8, in the process's order.
    fn vars(&self) -> Vec<(String, String)>;
}

/// The real process environment.
#[derive(Clone, Copy, Debug, Default)]
pub struct SystemEnv;

#[allow(clippy::disallowed_methods)] // the one place the real environment is read
impl Env for SystemEnv {
    fn var(&self, key: &str) -> Option<String> {
        std::env::var(key).ok()
    }

    fn vars(&self) -> Vec<(String, String)> {
        std::env::vars_os().filter_map(|(k, v)| Some((k.into_string().ok()?, v.into_string().ok()?))).collect()
    }
}

/// A fixed environment for tests.
#[derive(Clone, Debug, Default)]
pub struct MapEnv(pub BTreeMap<String, String>);

impl MapEnv {
    pub fn new<K: Into<String>, V: Into<String>>(pairs: impl IntoIterator<Item = (K, V)>) -> Self {
        MapEnv(pairs.into_iter().map(|(k, v)| (k.into(), v.into())).collect())
    }
}

impl Env for MapEnv {
    fn var(&self, key: &str) -> Option<String> {
        self.0.get(key).cloned()
    }

    fn vars(&self) -> Vec<(String, String)> {
        self.0.iter().map(|(k, v)| (k.clone(), v.clone())).collect()
    }
}

/// The environment with `TZ=UTC0` and `LC_ALL=C` forced, so tools print times and numbers the
/// same way on every host (Go's `proc.StableTimeEnv`).
pub fn stable_time_env(env: &dyn Env) -> Vec<(String, String)> {
    let mut out: Vec<(String, String)> = env.vars().into_iter().filter(|(k, _)| k != "TZ" && k != "LC_ALL").collect();
    out.push(("TZ".into(), "UTC0".into()));
    out.push(("LC_ALL".into(), "C".into()));
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn map_env_reads_what_it_was_given() {
        let env = MapEnv::new([("HOME", "/Users/x")]);
        assert_eq!(env.var("HOME").as_deref(), Some("/Users/x"));
        assert_eq!(env.var("PATH"), None);
    }

    #[test]
    fn stable_time_env_replaces_tz_and_locale() {
        let env = MapEnv::new([("TZ", "America/Chicago"), ("LC_ALL", "de_DE"), ("PATH", "/bin")]);
        let got = stable_time_env(&env);
        assert_eq!(got, vec![("PATH".into(), "/bin".into()), ("TZ".into(), "UTC0".into()), ("LC_ALL".into(), "C".into())]);
    }
}
