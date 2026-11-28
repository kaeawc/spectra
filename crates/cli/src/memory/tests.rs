//! Ported from Go's `cmd/spectra/memory_test.go`, plus the watch loop, which the recorded cases
//! can't cover (it runs until a sample fails).

use std::sync::{Arc, Mutex};

use spectra_base::clock::{Clock, FakeClock, Sleeper};
use spectra_base::env::MapEnv;
use spectra_base::hostos::Kind;
use spectra_host::memstate::{DarwinSample, DarwinVmCounts, Error, MemorySource};

use super::*;

const S: i64 = 1_000_000_000;

// Go: TestParseMemoryWatch.
#[test]
fn parse_watch_table() {
    for (input, want) in [("", 0), ("1", S), ("5s", 5 * S), ("1m30s", 90 * S), ("+2", 2 * S), ("010", 10 * S)] {
        assert_eq!(parse_watch(input), Ok(GoDuration(want)), "{input:?}");
    }
}

// Go: TestParseMemoryWatchRejectsNonPositive.
#[test]
fn parse_watch_rejects_non_positive() {
    for input in ["0", "-1s", "-3"] {
        assert_eq!(parse_watch(input), Err("--watch must be positive".to_string()), "{input:?}");
    }
    assert_eq!(parse_watch("0x10"), Err(r#"invalid --watch: time: unknown unit "x" in duration "0x10""#.to_string()), "Atoi is base 10");
}

// Go: TestMemoryRate.
#[test]
fn rates() {
    assert_eq!(rate(30, 10, 2.0), 10.0);
    assert_eq!(rate(10, 30, 2.0), 0.0, "a counter reset");
}

#[test]
fn memory_bytes_units() {
    for (n, want) in [(0, "0B"), (1023, "1023B"), (1536, "2KB"), (5 << 20, "5MB"), (3 << 29, "1.5GB")] {
        assert_eq!(memory_bytes(n), want, "{n}");
    }
}

/// Samples until `fail_after` calls, then fails; each sample's counters grow.
struct Counting {
    calls: Mutex<u64>,
    fail_after: u64,
}

impl MemorySource for Counting {
    fn darwin(&self) -> Result<DarwinSample, Error> {
        let mut n = self.calls.lock().unwrap_or_else(std::sync::PoisonError::into_inner);
        *n += 1;
        if *n > self.fail_after {
            return Err(Error::VmStatistics(5));
        }
        let vm = DarwinVmCounts { compressions: 100 * *n, swapouts: 10 * *n, ..DarwinVmCounts::default() };
        Ok(DarwinSample { page_size_bytes: 4096, physical_bytes: 1 << 30, vm, memorystatus_level: Some(50), ..DarwinSample::default() })
    }

    fn page_size(&self) -> u64 {
        4096
    }
}

/// A sleeper that moves the fake clock, so the ticker sees time pass.
struct Advancing {
    clock: Arc<FakeClock>,
    slept: Mutex<Vec<Duration>>,
}

impl Sleeper for Advancing {
    fn sleep(&self, d: Duration) {
        self.slept.lock().unwrap_or_else(std::sync::PoisonError::into_inner).push(d);
        self.clock.advance(d);
    }
}

fn watch(args: &[&str], fail_after: u64) -> (i32, String, String, Vec<Duration>) {
    let clock = Arc::new(FakeClock::new("2026-11-23T10:00:00Z".parse().expect("time")));
    let sleeper = Arc::new(Advancing { clock: Arc::clone(&clock), slept: Mutex::new(Vec::new()) });
    let ctx = Ctx {
        os: Kind::Darwin,
        env: Arc::new(MapEnv::default()),
        clock: clock.clone() as Arc<dyn Clock>,
        sleeper: sleeper.clone() as Arc<dyn Sleeper>,
        memory: Arc::new(Counting { calls: Mutex::new(0), fail_after }),
        ..Ctx::system("v")
    };
    let args: Vec<String> = args.iter().map(|s| s.to_string()).collect();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = crate::run(&args, &ctx, &mut out, &mut err);
    let slept = sleeper.slept.lock().unwrap_or_else(std::sync::PoisonError::into_inner).clone();
    (code, String::from_utf8(out).expect("utf8"), String::from_utf8(err).expect("utf8"), slept)
}

#[test]
fn watch_text_prints_rates_after_the_first_sample() {
    let (code, out, err, slept) = watch(&["memory", "--watch", "2"], 3);
    assert_eq!((code, err.as_str()), (1, "host_statistics64: kern_return=5\n"));
    let lines: Vec<&str> = out.lines().collect();
    assert_eq!(lines.len(), 3, "{out}");
    assert!(lines[0].ends_with("free=50%  pages_free=0%"), "the first line has no rates: {}", lines[0]);
    assert!(lines[1].ends_with("free=50%  pages_free=0%  compressions/s=50  swapouts/s=5"), "{}", lines[1]);
    assert_eq!(slept, vec![Duration::from_secs(2); 3], "one tick per interval");
}

#[test]
fn watch_json_is_one_compact_document_per_sample() {
    let (code, out, _, _) = watch(&["memory", "--json", "--watch=1s"], 2);
    assert_eq!(code, 1);
    let lines: Vec<&str> = out.lines().collect();
    assert_eq!(lines.len(), 2);
    assert!(lines.iter().all(|l| l.starts_with("{\"physical_bytes\":1073741824,")), "{out}");
    assert!(lines[1].contains("\"compressions\":200,") && lines[1].ends_with("\"collected_at\":\"2026-11-23T10:00:01Z\"}"), "{}", lines[1]);
}

#[test]
fn a_slow_sample_does_not_make_up_missed_ticks() {
    // The clock jumps 3.5 intervals while sampling: the next tick comes at the next point of the
    // schedule, as Go's ticker drops the ticks a slow reader missed.
    struct Slow(Arc<FakeClock>, Counting);
    impl MemorySource for Slow {
        fn darwin(&self) -> Result<DarwinSample, Error> {
            self.0.advance(Duration::from_millis(3500));
            self.1.darwin()
        }
        fn page_size(&self) -> u64 {
            4096
        }
    }
    let clock = Arc::new(FakeClock::new(jiff::Timestamp::UNIX_EPOCH));
    let sleeper = Arc::new(Advancing { clock: Arc::clone(&clock), slept: Mutex::new(Vec::new()) });
    let ctx = Ctx {
        os: Kind::Darwin,
        clock: clock.clone() as Arc<dyn Clock>,
        sleeper: sleeper.clone() as Arc<dyn Sleeper>,
        memory: Arc::new(Slow(Arc::clone(&clock), Counting { calls: Mutex::new(0), fail_after: 2 })),
        ..Ctx::system("v")
    };
    let (mut out, mut err) = (Vec::new(), Vec::new());
    assert_eq!(crate::run(&["memory".into(), "--watch".into(), "1".into()], &ctx, &mut out, &mut err), 1);
    // Ticks at 1s, 2s, 3s pass during the first sample (now 3.5s): it goes on at once, and the
    // next tick is at 4s; the second sample ends at 7s, past it, so again no sleep.
    assert_eq!(*sleeper.slept.lock().unwrap_or_else(std::sync::PoisonError::into_inner), Vec::<Duration>::new());
}

#[test]
fn linux_text_prints_the_reason_only_for_the_first_sample() {
    let s = MemoryState {
        os: "linux".into(),
        physical_bytes: 4 << 30,
        available_bytes: 1 << 30,
        pressure_free_percent: 25.0,
        pressure_level: PressureLevel::Normal,
        pressure_reason: "25.0% of memory available".into(),
        collected_at: GoTime::utc("2026-11-23T10:00:02Z".parse().expect("time")),
        swap_ins: 30,
        ..MemoryState::default()
    };
    let mut out = Vec::new();
    write_state(&mut out, &s, &MemoryState::default());
    assert_eq!(
        String::from_utf8(out).expect("utf8"),
        "phys=4.0GB  available=1.0GB(25%)  anon=0B  file=0B  swap=0B/0B  pressure=Normal\npressure: 25.0% of memory available\n"
    );
    let prev = MemoryState { collected_at: GoTime::utc("2026-11-23T10:00:00Z".parse().expect("time")), swap_ins: 10, ..s.clone() };
    let mut out = Vec::new();
    write_state(&mut out, &s, &prev);
    assert!(String::from_utf8(out).expect("utf8").ends_with("pressure=Normal  swapins/s=10  swapouts/s=0\n"));
}
