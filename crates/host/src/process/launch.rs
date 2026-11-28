//! Another process's working directory (Go's `launch.go`): the `/proc/<pid>/cwd` link on Linux,
//! one single-pid `lsof` on macOS.

use std::path::Path;
use std::time::Duration;

use spectra_base::fs::Fs;
use spectra_base::hostos::{Kind, Unsupported};
use spectra_base::proc::{Cmd, Runner};

use crate::goerr;

const CWD_TIMEOUT: Duration = Duration::from_secs(2);

/// Reads another process's working directory (Go's `CwdReader`).
pub struct CwdReader<'a> {
    /// [`Kind::Unknown`] means the host.
    pub os: Kind,
    pub runner: &'a dyn Runner,
    pub fs: &'a dyn Fs,
}

impl CwdReader<'_> {
    /// `pid`'s working directory.
    pub fn cwd(&self, pid: i64) -> Result<String, String> {
        self.cwd_on(self.os.resolve(), pid)
    }

    fn cwd_on(&self, os: Kind, pid: i64) -> Result<String, String> {
        match os {
            Kind::Linux => {
                let link = format!("/proc/{pid}/cwd");
                let d = self
                    .fs
                    .read_link(Path::new(&link))
                    .map_err(|e| format!("cwd pid {pid}: {}", goerr::path_error("readlink", &link, &e)))?;
                Ok(d.to_string_lossy().into_owned())
            }
            Kind::Darwin => {
                let cmd = Cmd::new("lsof", ["-a".to_string(), "-p".into(), pid.to_string(), "-d".into(), "cwd".into(), "-Fn".into()])
                    .timeout(CWD_TIMEOUT);
                let (out, err) = match self.runner.run(&cmd) {
                    Ok(o) if o.exit_code == 0 => (o.stdout, None),
                    Ok(o) => (o.stdout, Some(format!("exit status {}", o.exit_code))),
                    Err(e) => (Vec::new(), Some(e.to_string())),
                };
                if let Some(e) = err
                    && out.is_empty()
                {
                    return Err(format!("cwd pid {pid}: {e}"));
                }
                parse_lsof_name(&String::from_utf8_lossy(&out))
            }
            Kind::Unknown => Err(Unsupported::new("process working directory", os.resolve()).to_string()),
        }
    }
}

/// The first `n<path>` field of `lsof -F` output.
pub fn parse_lsof_name(out: &str) -> Result<String, String> {
    out.split('\n')
        .find_map(|l| l.strip_prefix('n').filter(|rest| !rest.is_empty()))
        .map(str::to_string)
        .ok_or_else(|| "lsof: no cwd in output".to_string())
}

#[cfg(test)]
mod tests {
    use std::io;
    use std::path::PathBuf;
    use std::sync::Mutex;

    use spectra_base::fs::{DirEntry, FileInfo};
    use spectra_base::proc::{FakeRunner, Output, match_exact};

    use super::*;

    struct LinkFs {
        target: Result<String, ()>,
        asked: Mutex<Vec<PathBuf>>,
    }

    impl Fs for LinkFs {
        fn read(&self, _: &Path) -> io::Result<Vec<u8>> {
            Err(io::Error::from_raw_os_error(2))
        }
        fn read_dir(&self, _: &Path) -> io::Result<Vec<DirEntry>> {
            Err(io::Error::from_raw_os_error(2))
        }
        fn metadata(&self, _: &Path) -> io::Result<FileInfo> {
            Err(io::Error::from_raw_os_error(2))
        }
        fn symlink_metadata(&self, _: &Path) -> io::Result<FileInfo> {
            Err(io::Error::from_raw_os_error(2))
        }
        fn read_link(&self, p: &Path) -> io::Result<PathBuf> {
            self.asked.lock().expect("lock").push(p.to_path_buf());
            self.target.clone().map(PathBuf::from).map_err(|()| io::Error::from_raw_os_error(13))
        }
    }

    // Go: TestCwdLinux.
    #[test]
    fn cwd_linux() {
        let runner = FakeRunner::new();
        let fs = LinkFs { target: Ok("/srv/app".into()), asked: Mutex::new(vec![]) };
        let c = CwdReader { os: Kind::Linux, runner: &runner, fs: &fs };
        assert_eq!(c.cwd(42), Ok("/srv/app".to_string()));
        assert_eq!(fs.asked.lock().expect("lock").as_slice(), [PathBuf::from("/proc/42/cwd")]);
        let denied = LinkFs { target: Err(()), asked: Mutex::new(vec![]) };
        let err = CwdReader { os: Kind::Linux, runner: &runner, fs: &denied }.cwd(42).expect_err("denied");
        assert_eq!(err, "cwd pid 42: readlink /proc/42/cwd: permission denied");
    }

    // Go: TestCwdDarwin.
    #[test]
    fn cwd_darwin() {
        let runner = FakeRunner::new();
        runner.on_stdout(match_exact("lsof", &["-a", "-p", "42", "-d", "cwd", "-Fn"]), "p42\nfcwd\nn/Users/jane/My App\n");
        let fs = LinkFs { target: Err(()), asked: Mutex::new(vec![]) };
        assert_eq!(CwdReader { os: Kind::Darwin, runner: &runner, fs: &fs }.cwd(42), Ok("/Users/jane/My App".to_string()));
        assert_eq!(runner.calls().len(), 1);
        let failing = FakeRunner::new();
        failing.on(match_exact("lsof", &["-a", "-p", "42", "-d", "cwd", "-Fn"]), Output { exit_code: 1, ..Output::default() });
        assert!(CwdReader { os: Kind::Darwin, runner: &failing, fs: &fs }.cwd(42).is_err(), "lsof failure swallowed");
    }

    // Go: TestCwdUnsupported (an OS that is neither, which only Kind::Unknown unresolved is).
    #[test]
    fn cwd_unsupported() {
        let runner = FakeRunner::new();
        let fs = LinkFs { target: Ok("/x".into()), asked: Mutex::new(vec![]) };
        assert!(CwdReader { os: Kind::Unknown, runner: &runner, fs: &fs }.cwd_on(Kind::Unknown, 1).is_err());
    }
}
