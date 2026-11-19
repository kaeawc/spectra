//! The JVM feed: `spectra jvm -json`, parsed and reduced to the numbers the app tracks.

use serde::Deserialize;
use std::process::Command;

/// `jstat -gc` columns as Spectra reports them, in KiB and seconds.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Gc {
    pub s0c: f64,
    pub s1c: f64,
    pub s0u: f64,
    pub s1u: f64,
    pub ec: f64,
    pub eu: f64,
    pub oc: f64,
    pub ou: f64,
    pub mc: f64,
    pub mu: f64,
    pub ygc: u64,
    pub fgc: u64,
    pub fgct: f64,
    pub gct: f64,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Classes {
    pub loaded: u64,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct Jvm {
    pub pid: u32,
    pub main_class: String,
    pub jdk_vendor: String,
    pub jdk_version: String,
    pub vm_args: String,
    pub thread_count: u64,
    pub gc: Option<Gc>,
    pub classes: Option<Classes>,
}

impl Jvm {
    /// A short name: the class or jar without its package or folder.
    pub fn name(&self) -> String {
        let last = self.main_class.rsplit(['/', '.']).find(|p| !p.is_empty() && *p != "jar");
        match last {
            Some(n) if self.main_class.ends_with(".jar") => format!("{n}.jar"),
            Some(n) => n.to_owned(),
            None => format!("pid {}", self.pid),
        }
    }

    pub fn heap_used_kib(&self) -> f64 {
        self.gc.as_ref().map_or(0.0, |g| g.eu + g.ou + g.s0u + g.s1u)
    }

    /// `-Xmx` when set, else what the heap has committed so far.
    pub fn heap_max_kib(&self) -> f64 {
        flag_size_kib(&self.vm_args, "-Xmx")
            .unwrap_or_else(|| self.gc.as_ref().map_or(0.0, |g| g.ec + g.oc + g.s0c + g.s1c))
    }

    pub fn sample(&self) -> Sample {
        let g = self.gc.clone().unwrap_or_default();
        Sample {
            heap_used_kib: self.heap_used_kib(),
            heap_max_kib: self.heap_max_kib(),
            old_used_kib: g.ou,
            metaspace_kib: g.mu,
            gc_seconds: g.gct,
            gc_count: g.ygc + g.fgc,
            classes: self.classes.as_ref().map_or(0, |c| c.loaded),
            threads: self.thread_count,
        }
    }
}

/// One reading of a JVM, kept in history and as a compare baseline.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Sample {
    pub heap_used_kib: f64,
    pub heap_max_kib: f64,
    pub old_used_kib: f64,
    pub metaspace_kib: f64,
    pub gc_seconds: f64,
    pub gc_count: u64,
    pub classes: u64,
    pub threads: u64,
}

/// The size after `prefix` in JVM arguments (`-Xmx5g`, `-XX:MaxMetaspaceSize=256m`), in KiB.
pub fn flag_size_kib(vm_args: &str, prefix: &str) -> Option<f64> {
    let raw = vm_args.split_whitespace().rev().find_map(|a| a.strip_prefix(prefix))?;
    let raw = raw.trim_start_matches('=');
    let digits = raw.trim_end_matches(|c: char| c.is_ascii_alphabetic());
    let n: f64 = digits.parse().ok()?;
    let kib = match raw[digits.len()..].to_ascii_lowercase().as_str() {
        "" => n / 1024.0,
        "k" => n,
        "m" => n * 1024.0,
        "g" => n * 1024.0 * 1024.0,
        _ => return None,
    };
    Some(kib)
}

pub fn parse(json: &str) -> Result<Vec<Jvm>, String> {
    let mut jvms: Vec<Jvm> = serde_json::from_str(json).map_err(|e| format!("Couldn't read Spectra's output: {e}"))?;
    // The JDK tools Spectra runs to look are JVMs too; they're gone by the next reading.
    jvms.retain(|j| !j.main_class.starts_with("jdk.jcmd/"));
    jvms.sort_by_key(|j| j.pid);
    Ok(jvms)
}

/// Runs `spectra jvm -json`. Blocks for as long as Spectra takes; call it off the UI thread.
pub fn fetch() -> Result<Vec<Jvm>, String> {
    let bin = std::env::var("SPECTRA_BIN").unwrap_or_else(|_| "spectra".into());
    let out = Command::new(&bin)
        .args(["jvm", "-json"])
        .output()
        .map_err(|e| format!("Couldn't run {bin}: {e}. Install Spectra or set SPECTRA_BIN."))?;
    if !out.status.success() {
        return Err(format!("{bin} jvm failed: {}", String::from_utf8_lossy(&out.stderr).trim()));
    }
    parse(&String::from_utf8_lossy(&out.stdout))
}

pub fn fmt_kib(kib: f64) -> String {
    if kib >= 1024.0 * 1024.0 {
        format!("{:.1} GB", kib / 1024.0 / 1024.0)
    } else {
        format!("{:.0} MB", kib / 1024.0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_flag_sizes() {
        assert_eq!(flag_size_kib("-Xms512m -Xmx5g", "-Xmx"), Some(5.0 * 1024.0 * 1024.0));
        assert_eq!(flag_size_kib("-XX:MaxMetaspaceSize=256m", "-XX:MaxMetaspaceSize"), Some(256.0 * 1024.0));
        assert_eq!(flag_size_kib("-Xmx1g -Xmx2g", "-Xmx"), Some(2.0 * 1024.0 * 1024.0));
        assert_eq!(flag_size_kib("-Dfoo=bar", "-Xmx"), None);
    }

    #[test]
    fn names_classes_and_jars() {
        let j = |m: &str| Jvm { pid: 7, main_class: m.into(), ..Jvm::default() };
        assert_eq!(j("org.gradle.launcher.daemon.bootstrap.GradleDaemon").name(), "GradleDaemon");
        assert_eq!(j("/opt/app/gradle-wrapper.jar").name(), "gradle-wrapper.jar");
        assert_eq!(j("").name(), "pid 7");
    }

    #[test]
    fn parses_and_drops_jdk_tools() {
        let src = r#"[{"pid":2,"main_class":"jdk.jcmd/sun.tools.jps.Jps"},
            {"pid":1,"main_class":"a.B","vm_args":"-Xmx1g","gc":{"eu":1024,"ou":2048}}]"#;
        let jvms = parse(src).unwrap();
        assert_eq!(jvms.len(), 1);
        assert_eq!(jvms[0].heap_used_kib(), 3072.0);
        assert_eq!(jvms[0].heap_max_kib(), 1024.0 * 1024.0);
    }

    /// Needs Spectra installed: `cargo test -- --ignored`.
    #[test]
    #[ignore]
    fn fetches_the_live_feed() {
        let jvms = fetch().unwrap();
        for j in &jvms {
            println!("{} {} heap {} of {}", j.pid, j.name(), fmt_kib(j.heap_used_kib()), fmt_kib(j.heap_max_kib()));
        }
    }
}
