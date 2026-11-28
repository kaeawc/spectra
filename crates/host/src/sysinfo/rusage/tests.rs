use std::collections::BTreeMap;
use std::sync::Mutex;

use spectra_base::clock::FakeSleeper;

use super::*;

/// A reader answering from a closure.
struct FnReader<F: Fn(i64) -> Result<ProcRusage, RusageError> + Send + Sync>(F);

impl<F: Fn(i64) -> Result<ProcRusage, RusageError> + Send + Sync> RusageSource for FnReader<F> {
    fn read(&self, pid: i64) -> Result<ProcRusage, RusageError> {
        (self.0)(pid)
    }
}

fn sampler<'a>(interval: Duration, reader: &'a dyn RusageSource, sleeper: &'a dyn Sleeper) -> EnergySampler<'a> {
    EnergySampler { interval, reader, sleeper, cancel: None }
}

fn pids(deltas: &[EnergyDelta]) -> Vec<i64> {
    deltas.iter().map(|d| d.pid).collect()
}

// Go: TestEnergySamplerHappyPath.
#[test]
fn happy_path_ranks_by_billed_energy() {
    let calls = Mutex::new(0);
    let reader = FnReader(|pid| {
        let mut n = calls.lock().expect("lock");
        *n += 1;
        let mut base = ProcRusage { pid, billed_energy_nj: 1000, energy_nj: 1100, interrupt_wakeups: 50, ..ProcRusage::default() };
        if *n > 2 {
            let p = u64::try_from(pid).expect("pid");
            base.billed_energy_nj += p * 1_000_000;
            base.energy_nj += p * 1_100_000;
            base.interrupt_wakeups += p * 100;
            base.user_ns += p * 500_000;
        }
        Ok(base)
    });
    let sleeper = FakeSleeper::new();
    let deltas = sampler(Duration::from_millis(100), &reader, &sleeper).sample(&[1, 2]).expect("sample");
    assert_eq!(deltas.len(), 2);
    let ranked = ranked_energy(&deltas, 0);
    assert_eq!(pids(&ranked), [2, 1]);
    assert_eq!(ranked[0].billed_energy_nj, 2_000_000);
    assert_eq!(ranked[0].interval, GoDuration(100_000_000));
    assert_eq!(sleeper.slept(), [Duration::from_millis(100)]);
}

// Go: TestEnergySamplerZeroDelta.
#[test]
fn zero_delta_for_a_sleeping_pid() {
    let frozen = ProcRusage { pid: 7, billed_energy_nj: 999_999, interrupt_wakeups: 10, ..ProcRusage::default() };
    let reader = FnReader(move |_| Ok(frozen));
    let deltas = sampler(Duration::from_millis(1), &reader, &FakeSleeper::new()).sample(&[7]).expect("sample");
    assert_eq!(deltas.len(), 1);
    let d = &deltas[0];
    assert_eq!((d.billed_energy_nj, d.interrupt_wakeups, d.energy_nj), (0, 0, 0));
}

// Go: TestEnergySamplerUnsupportedPropagates.
#[test]
fn unsupported_propagates_before_the_wait() {
    let reader = FnReader(|_| Err(RusageError::Unsupported));
    let sleeper = FakeSleeper::new();
    let err = sampler(Duration::from_secs(1), &reader, &sleeper).sample(&[1, 2]).expect_err("unsupported");
    assert_eq!(err, SampleError::Unsupported);
    assert!(sleeper.slept().is_empty(), "sampler waited the interval before reporting unsupported");
}

// Go: TestEnergySamplerEPermSkipped.
#[test]
fn permission_errors_skip_the_pid() {
    let reader = FnReader(|pid| {
        if pid == 1 { Err(RusageError::Permission { pid }) } else { Ok(ProcRusage { pid, billed_energy_nj: 42, ..ProcRusage::default() }) }
    });
    let deltas = sampler(Duration::from_millis(1), &reader, &FakeSleeper::new()).sample(&[1, 2]).expect("sample");
    assert_eq!(pids(&deltas), [2]);
}

// Go: TestEnergySamplerCtxCancel.
#[test]
fn a_cancelled_sampler_stops_before_waiting() {
    let reader = FnReader(|pid| Ok(ProcRusage { pid, ..ProcRusage::default() }));
    let token = CancelToken::new();
    token.cancel();
    let sleeper = FakeSleeper::new();
    let s = EnergySampler { interval: Duration::from_secs(5), reader: &reader, sleeper: &sleeper, cancel: Some(token.clone()) };
    assert_eq!(s.sample(&[1]).expect_err("cancelled"), SampleError::Canceled);
    assert!(sleeper.slept().is_empty());
    // Go's defaultSleep returns at once for a zero interval, cancelled or not.
    let s = EnergySampler { interval: Duration::ZERO, reader: &reader, sleeper: &sleeper, cancel: Some(token) };
    assert_eq!(s.sample(&[1]).expect("zero interval").len(), 1);
}

// Go: TestRankedEnergyTopN.
#[test]
fn ranked_energy_cuts_to_top_n_and_breaks_ties_on_wakeups() {
    let d = |pid, billed, wake| EnergyDelta { pid, billed_energy_nj: billed, interrupt_wakeups: wake, ..EnergyDelta::default() };
    let input = [d(1, 100, 0), d(2, 300, 0), d(3, 200, 0), d(4, 300, 99)];
    assert_eq!(pids(&ranked_energy(&input, 2)), [4, 2]);
    assert_eq!(pids(&ranked_energy(&input, 0)), [4, 2, 3, 1]);
}

#[test]
fn ranked_energy_ties_fall_to_disk_bytes_then_pid() {
    let d = |pid, read, written| EnergyDelta { pid, disk_bytes_read: read, disk_bytes_written: written, ..EnergyDelta::default() };
    assert_eq!(pids(&ranked_energy(&[d(9, 1, 1), d(8, 5, 0), d(7, 1, 1)], 0)), [8, 7, 9]);
}

#[test]
fn counters_that_went_backwards_clamp_to_zero() {
    let before = ProcRusage { user_ns: 10, ..ProcRusage::default() };
    let after = ProcRusage { user_ns: 4, system_ns: 3, ..ProcRusage::default() };
    let d = diff_one(1, &before, &after, GoDuration(0));
    assert_eq!((d.user_ns, d.system_ns), (0, 3));
}

#[test]
fn pids_gone_by_the_second_pass_are_dropped() {
    let seen: Mutex<BTreeMap<i64, u32>> = Mutex::new(BTreeMap::new());
    let reader = FnReader(|pid| {
        let mut m = seen.lock().expect("lock");
        let n = m.entry(pid).or_insert(0);
        *n += 1;
        if pid == 3 && *n > 1 { Err(RusageError::Other("gone".into())) } else { Ok(ProcRusage { pid, ..ProcRusage::default() }) }
    });
    let deltas = sampler(Duration::from_millis(1), &reader, &FakeSleeper::new()).sample(&[1, 3]).expect("sample");
    assert_eq!(pids(&deltas), [1]);
}

// Go: TestReadRusageSelf (macOS only).
#[cfg(target_os = "macos")]
#[test]
fn host_rusage_reads_the_current_process() {
    let pid = i64::from(std::process::id());
    let r = HostRusage.read(pid).expect("proc_pid_rusage(self)");
    assert_eq!(r.pid, pid);
    assert!(r.user_ns != 0 || r.system_ns != 0, "expected non-zero CPU time for self, got {r:?}");
}

#[cfg(not(target_os = "macos"))]
#[test]
fn host_rusage_is_unsupported_off_macos() {
    assert!(matches!(HostRusage.read(1), Err(RusageError::Unsupported)));
}

#[test]
fn rusage_serializes_like_go() {
    let r = ProcRusage { pid: 5, billed_energy_nj: 7, ..ProcRusage::default() };
    let json = String::from_utf8(spectra_base::gojson::to_vec(&r).expect("json")).expect("utf8");
    assert_eq!(
        json,
        r#"{"pid":5,"billed_energy_nj":7,"serviced_energy_nj":0,"interrupt_wakeups":0,"pkg_idle_wakeups":0,"user_ns":0,"system_ns":0,"disk_bytes_read":0,"disk_bytes_written":0}"#
    );
}
