use std::cell::RefCell;
use std::collections::BTreeMap;

use spectra_base::clock::FakeClock;

use super::*;

/// Mimics `sample -file`: the report goes to the named file, not stdout.
struct FakeCommands<'a> {
    output: Option<Vec<u8>>,
    err: Option<String>,
    calls: RefCell<Vec<(String, Vec<String>)>>,
    files: &'a MemFiles,
}

impl CommandRunner for FakeCommands<'_> {
    fn run(&self, name: &str, args: &[String], _timeout: Duration) -> (Vec<u8>, Option<String>) {
        self.calls.borrow_mut().push((name.to_string(), args.to_vec()));
        if let (Some(path), Some(out)) = (file_arg(args), &self.output) {
            self.files.write(&path, out);
        }
        (b"Sampling process...\n".to_vec(), self.err.clone())
    }
}

/// An in-memory [`ReportFiles`] that records removals.
#[derive(Default)]
struct MemFiles {
    files: RefCell<BTreeMap<String, Vec<u8>>>,
    next: RefCell<usize>,
    create_err: Option<String>,
    read_err: Option<String>,
    removed: RefCell<Vec<String>>,
}

impl MemFiles {
    fn write(&self, path: &str, data: &[u8]) {
        if let Some(f) = self.files.borrow_mut().get_mut(path) {
            *f = data.to_vec();
        }
    }

    fn assert_cleaned_up(&self) {
        assert!(self.files.borrow().is_empty(), "leftover report files: {:?}", self.files.borrow());
        assert_eq!(*self.next.borrow(), self.removed.borrow().len(), "created vs removed");
    }
}

impl ReportFiles for MemFiles {
    fn create_temp(&self, pattern: &str) -> io::Result<String> {
        if let Some(e) = &self.create_err {
            return Err(io::Error::other(e.clone()));
        }
        *self.next.borrow_mut() += 1;
        let path = format!("/mem/{}-{pattern}", self.next.borrow());
        self.files.borrow_mut().insert(path.clone(), Vec::new());
        Ok(path)
    }
    fn read_file(&self, path: &str) -> io::Result<Vec<u8>> {
        if let Some(e) = &self.read_err {
            return Err(io::Error::other(e.clone()));
        }
        self.files.borrow().get(path).cloned().ok_or_else(|| io::Error::from_raw_os_error(2))
    }
    fn remove(&self, path: &str) -> io::Result<()> {
        self.removed.borrow_mut().push(path.to_string());
        self.files.borrow_mut().remove(path);
        Ok(())
    }
}

fn file_arg(args: &[String]) -> Option<String> {
    args.windows(2).find(|w| w[0] == "-file").map(|w| w[1].clone())
}

fn sample_args(args: &[String]) -> Vec<String> {
    let n = args.len();
    assert!(n >= 2 && args[n - 2] == "-file", "args = {args:?}, want trailing -file <path>");
    args[..n - 2].to_vec()
}

fn commands<'a>(files: &'a MemFiles, output: Option<&str>, err: Option<&str>) -> FakeCommands<'a> {
    FakeCommands { output: output.map(|o| o.as_bytes().to_vec()), err: err.map(str::to_string), calls: RefCell::new(vec![]), files }
}

fn clock() -> FakeClock {
    FakeClock::new("1970-01-01T00:01:40Z".parse().expect("ts"))
}

fn sampler<'a>(runner: &'a FakeCommands<'a>, store: Option<&'a dyn SampleStore>, clock: &'a FakeClock) -> Sampler<'a> {
    Sampler { os: Kind::Darwin, runner, store, clock, files: runner.files }
}

fn pid(pid: i64) -> SampleOptions {
    SampleOptions { pid, ..SampleOptions::default() }
}

// Go: TestSamplerCaptureUnsupportedOffDarwin.
#[test]
fn capture_unsupported_off_darwin() {
    let files = MemFiles::default();
    let runner = commands(&files, Some("x"), None);
    let c = clock();
    let s = Sampler { os: Kind::Linux, ..sampler(&runner, None, &c) };
    assert!(matches!(s.capture(pid(42)), Err(SampleError::Unsupported(_))));
}

// Go: TestRunSampleReportRemovesTempFile.
#[test]
fn report_temp_file_is_removed() {
    let files = MemFiles::default();
    let runner = commands(&files, Some("Call graph:\n"), None);
    let c = clock();
    let res = sampler(&runner, None, &c).capture(pid(42)).expect("capture");
    assert_eq!(res.output, "Call graph:\n");
    assert_eq!(file_arg(&runner.calls.borrow()[0].1), Some(files.removed.borrow()[0].clone()));
    files.assert_cleaned_up();
}

// Go: TestRunSampleReportRemovesTempFileOnRunError.
#[test]
fn report_removed_on_run_error() {
    let files = MemFiles::default();
    let runner = commands(&files, Some("partial"), Some("killed"));
    let c = clock();
    assert!(sampler(&runner, None, &c).capture(pid(42)).is_err());
    files.assert_cleaned_up();
}

// Go: TestRunSampleReportRemovesTempFileOnReadError.
#[test]
fn report_removed_on_read_error() {
    let files = MemFiles { read_err: Some("permission denied".into()), ..MemFiles::default() };
    let runner = commands(&files, Some("Call graph:\n"), None);
    let c = clock();
    let err = sampler(&runner, None, &c).capture(pid(42)).expect_err("read error").to_string();
    assert!(err.contains("read sample report") && err.contains("permission denied"), "{err}");
    files.assert_cleaned_up();
}

// Go: TestRunSampleReportCreateErrorSkipsRun.
#[test]
fn create_error_skips_run() {
    let files = MemFiles { create_err: Some("no space".into()), ..MemFiles::default() };
    let runner = commands(&files, Some("x"), None);
    let c = clock();
    let err = sampler(&runner, None, &c).capture(pid(42)).expect_err("create").to_string();
    assert!(err.contains("no space"), "{err}");
    assert!(runner.calls.borrow().is_empty(), "sample ran without a report file");
}

// Go: TestRunSampleReportReturnsStdoutOnError.
#[test]
fn run_error_keeps_stdout() {
    let files = MemFiles::default();
    let run = |_: &str, _: &[String]| (b"sample: requires root".to_vec(), Some("exit status 1".to_string()));
    let (_, stdout, err) = run_sample_report(&files, &run, &["1".into(), "1".into()]);
    assert!(err.is_some());
    assert_eq!(stdout, b"sample: requires root");
    files.assert_cleaned_up();
}

// Go: TestOSReportFilesRemovesReport.
#[test]
fn os_report_files_remove_the_report() {
    let dir = crate::process::test_dir("sample-report");
    let files = OsReportFiles { dir: dir.to_string_lossy().into_owned() };
    let run = |_: &str, args: &[String]| {
        let path = file_arg(args).expect("-file");
        spectra_base::fsutil::write_file_atomic(Path::new(&path), b"Call graph:\n", 0o600).expect("write");
        (Vec::new(), None)
    };
    let (report, _, err) = run_sample_report(&files, &run, &["1".into(), "1".into()]);
    assert_eq!((report.as_slice(), err), (&b"Call graph:\n"[..], None));
    assert!(OsFs.read_dir(&dir).expect("dir").is_empty(), "leftover files");
    std::fs::remove_dir_all(dir).expect("cleanup");
}

struct FakeStore {
    samples: RefCell<Vec<SampleResult>>,
    err: Option<String>,
}

impl SampleStore for FakeStore {
    fn put_sample(&self, sample: &SampleResult) -> Result<(), String> {
        self.samples.borrow_mut().push(sample.clone());
        self.err.clone().map_or(Ok(()), Err)
    }
}

// Go: TestSamplerCaptureRunsSampleCommand.
#[test]
fn capture_runs_sample() {
    let files = MemFiles::default();
    let runner = commands(&files, Some("Call graph:\n"), None);
    let store = FakeStore { samples: RefCell::new(vec![]), err: None };
    let c = clock();
    let opts = SampleOptions { pid: 4012, duration: GoDuration(5_000_000_000), interval_ms: 20, store: true, taken_at: None };
    let r = sampler(&runner, Some(&store), &c).capture(opts).expect("capture");
    assert_eq!((r.pid, r.duration_ms, r.interval_ms, r.output.as_str()), (4012, 5000, 20, "Call graph:\n"));
    assert_eq!(r.taken_at.to_string(), "1970-01-01T00:01:40Z");
    let calls = runner.calls.borrow();
    assert_eq!(calls.len(), 1);
    assert_eq!((calls[0].0.as_str(), sample_args(&calls[0].1)), ("sample", vec!["4012".to_string(), "5".into(), "20".into()]));
    assert_eq!(store.samples.borrow()[0].output, r.output);
}

// Go: TestSamplerCaptureDefaults.
#[test]
fn capture_defaults() {
    let files = MemFiles::default();
    let runner = commands(&files, Some("ok"), None);
    let c = clock();
    sampler(&runner, None, &c).capture(pid(99)).expect("capture");
    assert_eq!(sample_args(&runner.calls.borrow()[0].1), vec!["99".to_string(), "1".into(), "10".into()]);
}

// Go: TestSamplerCaptureRejectsInvalidPID.
#[test]
fn capture_rejects_invalid_pid() {
    let files = MemFiles::default();
    let runner = commands(&files, None, None);
    let c = clock();
    assert!(sampler(&runner, None, &c).capture(pid(0)).is_err());
}

// Go: TestSamplerCaptureWrapsRunnerError.
#[test]
fn capture_wraps_runner_error() {
    let files = MemFiles::default();
    let runner = commands(&files, None, Some("denied"));
    let c = clock();
    let err = sampler(&runner, None, &c).capture(pid(42)).expect_err("err").to_string();
    assert!(err.contains("sample pid 42"), "{err}");
}

// Go: TestSamplerCaptureReturnsStoreError.
#[test]
fn capture_returns_store_error() {
    let files = MemFiles::default();
    let runner = commands(&files, Some("ok"), None);
    let store = FakeStore { samples: RefCell::new(vec![]), err: Some("disk full".into()) };
    let c = clock();
    let err = sampler(&runner, Some(&store), &c).capture(SampleOptions { store: true, ..pid(42) }).expect_err("err").to_string();
    assert!(err.contains("store sample"), "{err}");
}
