//! The allowlist of kernel tunables Spectra captures (Go's `sysctls.go` and `linux.go`).

use std::collections::BTreeMap;
use std::path::Path;

use spectra_base::fs::Fs;
use spectra_base::hostos::Kind;
use spectra_base::proc::Runner;

use crate::cmdrun::succeeded;

/// Go's `AllowedSysctls`. Adding a key is a deliberate decision: `sysctl -a` is massive and
/// noisy.
pub const ALLOWED_SYSCTLS: &[&str] =
    &["kern.maxfiles", "kern.maxproc", "kern.ipc.maxsockbuf", "kern.boottime", "vm.memory_pressure", "hw.ncpu", "hw.memsize"];

/// Go's `LinuxAllowedSysctls`: tunables read from `/proc/sys`, dotted keys mapping to paths by
/// replacing `.` with `/`.
pub const LINUX_ALLOWED_SYSCTLS: &[&str] =
    &["fs.file-max", "fs.nr_open", "kernel.pid_max", "kernel.threads-max", "vm.swappiness", "vm.max_map_count", "net.core.somaxconn"];

/// Go's `CollectSysctls`: the allowed key to value map for `os`; keys absent on this kernel are
/// omitted.
pub fn collect_sysctls(os: Kind, runner: &dyn Runner, fs: &dyn Fs) -> BTreeMap<String, String> {
    if os.resolve() == Kind::Linux {
        return collect_sysctls_linux(fs, "/proc/sys");
    }
    let mut out = BTreeMap::new();
    for key in ALLOWED_SYSCTLS {
        let Some(val) = succeeded(runner, "sysctl", &["-n", key]) else { continue };
        let v = String::from_utf8_lossy(&val).trim().to_string();
        if !v.is_empty() {
            out.insert((*key).to_string(), v);
        }
    }
    out
}

/// Go's `collectSysctlsLinux`: the Linux allowlist from a `/proc/sys` tree rooted at
/// `proc_sys_root`. Missing keys are omitted.
pub fn collect_sysctls_linux(fs: &dyn Fs, proc_sys_root: &str) -> BTreeMap<String, String> {
    let mut out = BTreeMap::new();
    for key in LINUX_ALLOWED_SYSCTLS {
        let Ok(data) = fs.read(&Path::new(proc_sys_root).join(key.replace('.', "/"))) else { continue };
        let v = String::from_utf8_lossy(&data).trim().to_string();
        if !v.is_empty() {
            out.insert((*key).to_string(), v);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use spectra_base::proc::{FakeRunner, match_exact};

    use super::*;
    use crate::testfs::MapFs;

    fn stub(responses: &[(&str, &str)]) -> FakeRunner {
        let fake = FakeRunner::new();
        for (key, v) in responses {
            fake.on_stdout(match_exact("sysctl", &["-n", key]), format!("{v}\n"));
        }
        fake
    }

    // Go: TestCollectSysctls.
    #[test]
    fn collects_the_keys_the_stub_answers() {
        let runner = stub(&[("kern.maxfiles", "49152"), ("hw.ncpu", "16"), ("vm.memory_pressure", "0")]);
        let got = collect_sysctls(Kind::Darwin, &runner, &MapFs::default());
        assert_eq!(got["kern.maxfiles"], "49152");
        assert_eq!(got["hw.ncpu"], "16");
        assert!(!got.contains_key("kern.boottime"), "kern.boottime should be absent (not in stub)");
    }

    // Go: TestCollectSysctlsAllFail.
    #[test]
    fn all_failing_gives_an_empty_map() {
        assert!(collect_sysctls(Kind::Darwin, &FakeRunner::new(), &MapFs::default()).is_empty());
    }

    // Go: TestCollectSysctlsAllowlist.
    #[test]
    fn queries_every_allowed_key_and_no_others() {
        let runner = FakeRunner::new();
        for key in ALLOWED_SYSCTLS {
            runner.on_stdout(match_exact("sysctl", &["-n", key]), "0\n");
        }
        collect_sysctls(Kind::Darwin, &runner, &MapFs::default());
        let mut queried: Vec<String> = runner.calls().iter().map(|c| c.args[1].clone()).collect();
        queried.sort();
        let mut want: Vec<String> = ALLOWED_SYSCTLS.iter().map(|s| (*s).to_string()).collect();
        want.sort();
        assert_eq!(queried, want);
    }

    // Go: TestCollectSysctlsTrimWhitespace.
    #[test]
    fn trims_whitespace() {
        let runner = FakeRunner::new();
        runner.on_stdout(match_exact("sysctl", &["-n", "kern.maxfiles"]), "  49152  \n");
        let got = collect_sysctls(Kind::Darwin, &runner, &MapFs::default());
        assert_eq!(got["kern.maxfiles"], "49152");
    }

    #[test]
    fn a_blank_value_is_omitted() {
        let runner = stub(&[("kern.maxfiles", "   ")]);
        assert!(collect_sysctls(Kind::Darwin, &runner, &MapFs::default()).is_empty());
    }

    // Go: TestCollectSysctlsLinux.
    #[test]
    fn linux_reads_proc_sys_and_omits_absent_keys() {
        let fs = MapFs::new([
            ("/proc/sys/fs/file-max", "9223372036854775807\n"),
            ("/proc/sys/kernel/pid_max", "4194304\n"),
            ("/proc/sys/vm/swappiness", "60\n"),
        ]);
        let got = collect_sysctls_linux(&fs, "/proc/sys");
        assert_eq!(got["fs.file-max"], "9223372036854775807");
        assert_eq!(got["kernel.pid_max"], "4194304");
        assert_eq!(got["vm.swappiness"], "60");
        assert!(!got.contains_key("net.core.somaxconn"), "absent key should be omitted, not empty");
        // The OS picks the backend.
        let via_os = collect_sysctls(Kind::Linux, &FakeRunner::new(), &fs);
        assert_eq!(via_os, got);
    }
}
