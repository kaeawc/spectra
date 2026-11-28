//! Ported from Go's `internal/memstate/memstate_test.go` and `linux_test.go`.

use spectra_base::clock::FakeClock;

use super::*;
use crate::pressure::Line;
use crate::testfs::MapFs;

// Go: TestParseVMStat.
#[test]
fn parses_vm_stat() {
    const PAGE: u64 = 4096;
    let input = "Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                               10.
Pages active:                             20.
Pages inactive:                           30.
Pages speculative:                        40.
Pages wired down:                         50.
Pages purgeable:                          60.
File-backed pages:                        70.
Anonymous pages:                          80.
Pages stored in compressor:               90.
Pages occupied by compressor:             45.
Compressions:                             1.234.
Decompressions:                           234.
Swapins:                                  12.
Swapouts:                                 34.
";
    let got = parse_vm_stat(input, PAGE, 1024 * 1024);
    assert_eq!((got.free, got.active, got.inactive), (10 * PAGE, 20 * PAGE, 30 * PAGE));
    assert_eq!((got.speculative, got.wired, got.purgeable), (40 * PAGE, 50 * PAGE, 60 * PAGE));
    assert_eq!((got.file_backed, got.anonymous), (70 * PAGE, 80 * PAGE));
    assert_eq!((got.compressor_stored, got.compressor_occupied, got.compressor_ratio), (90 * PAGE, 45 * PAGE, 2.0));
    assert_eq!((got.compressions, got.decompressions, got.swap_ins, got.swap_outs), (1234, 234, 12, 34));
    assert_eq!(got.pressure_level, PressureLevel::Unknown);
}

// Go: TestParseXSWUsage.
#[test]
fn parses_xsw_usage() {
    let mut buf = Vec::new();
    for v in [100u64, 40, 60] {
        buf.extend_from_slice(&v.to_le_bytes());
    }
    buf.extend_from_slice(&1u32.to_le_bytes());
    let got = parse_xsw_usage(&buf).expect("parse");
    assert_eq!(got, SwapUsage { total_bytes: 100, free_bytes: 40, used_bytes: 60, encrypted: true });
    assert_eq!(parse_xsw_usage(&buf[..27]).expect_err("short"), "xsw_usage fixture too short: 27 bytes");
}

// Go: TestPressureLevel.
#[test]
fn pressure_levels() {
    for (raw, want) in [(1, PressureLevel::Normal), (2, PressureLevel::Warning), (4, PressureLevel::Critical), (99, PressureLevel::Unknown)]
    {
        assert_eq!(PressureLevel::from_raw(raw), want, "{raw}");
    }
    assert_eq!(PressureLevel::default().as_str(), "", "Go's zero value");
}

// Go: TestApplyMemorystatusLevel (PR #755).
#[test]
fn memorystatus_level_replaces_the_page_fraction() {
    const PAGE: u64 = 4096;
    let ms = parse_vm_stat(
        "Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 100.\nPages speculative: 21.\n",
        PAGE,
        1000 * PAGE,
    );
    assert_eq!((ms.free_pages_percent, ms.pressure_free_percent), (12.1, 12.1), "fallback");
    let mut got = ms.clone();
    apply_memorystatus_level(&mut got, 87);
    assert_eq!((got.pressure_free_percent, got.free_pages_percent), (87.0, 12.1));
    let mut bad = ms.clone();
    apply_memorystatus_level(&mut bad, 255);
    assert_eq!(bad.pressure_free_percent, ms.free_pages_percent, "an out-of-range level is ignored");
}

// Go: TestFromDarwinSample.
#[test]
fn assembles_a_darwin_sample() {
    const PAGE: u64 = 4096;
    let at = GoTime::utc("2026-11-23T10:00:00Z".parse().expect("time"));
    let mut s = DarwinSample {
        page_size_bytes: PAGE,
        physical_bytes: 1000 * PAGE,
        vm: DarwinVmCounts {
            free: 100,
            speculative: 21,
            compressor: 10,
            uncompressed_compressor: 25,
            swapouts: 3,
            ..DarwinVmCounts::default()
        },
        pressure_level: Some(2),
        ..DarwinSample::default()
    };
    let got = from_darwin_sample(&s, at);
    assert_eq!((got.free, got.compressor_ratio, got.swap_outs, got.collected_at), (100 * PAGE, 2.5, 3, at));
    assert_eq!((got.pressure_level, got.pressure_free_percent, got.free_pages_percent), (PressureLevel::Warning, 12.1, 12.1));
    s.memorystatus_level = Some(87);
    let got = from_darwin_sample(&s, at);
    assert_eq!((got.pressure_free_percent, got.free_pages_percent), (87.0, 12.1));
    s.pressure_level = None;
    assert_eq!(from_darwin_sample(&s, at).pressure_level, PressureLevel::Unknown);
}

const MEMINFO: &str = "MemTotal:        4000000 kB
MemFree:         1000000 kB
MemAvailable:    3000000 kB
Buffers:           50000 kB
Cached:          2000000 kB
Active:          1100000 kB
Inactive:        1500000 kB
Active(file):    1000000 kB
Inactive(file):  1000000 kB
Unevictable:           4 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
Zswap:            100000 kB
Zswapped:         300000 kB
AnonPages:        400000 kB
HugePages_Total:       0
";

fn psi_fixture(some10: &str, full10: &str) -> String {
    format!("some avg10={some10} avg60=0.00 avg300=0.00 total=1\nfull avg10={full10} avg60=0.00 avg300=0.00 total=1\n")
}

// Go: TestParseMeminfo.
#[test]
fn parses_meminfo() {
    let ms = parse_meminfo(MEMINFO.as_bytes()).expect("parse");
    const K: u64 = 1024;
    for (name, got, want) in [
        ("physical", ms.physical_bytes, 4000000 * K),
        ("free", ms.free, 1000000 * K),
        ("available", ms.available_bytes, 3000000 * K),
        ("file", ms.file_backed, 2000000 * K),
        ("anon", ms.anonymous, 400000 * K),
        ("swap total", ms.swap.total_bytes, 2000000 * K),
        ("swap used", ms.swap.used_bytes, 500000 * K),
        ("zswap", ms.compressor_occupied, 100000 * K),
        ("zswapped", ms.compressor_stored, 300000 * K),
    ] {
        assert_eq!(got, want, "{name}");
    }
    assert_eq!((ms.compressor_ratio, ms.pressure_free_percent, ms.os.as_str()), (3.0, 75.0, "linux"));
}

// Go: TestParseMeminfoWithoutMemAvailable.
#[test]
fn meminfo_without_mem_available() {
    let ms = parse_meminfo(b"MemTotal: 1000 kB\nMemFree: 100 kB\nCached: 200 kB\nBuffers: 50 kB\n").expect("parse");
    assert_eq!(ms.available_bytes, 350 * 1024, "free+cached+buffers");
}

// Go: TestParseMeminfoRejectsGarbage.
#[test]
fn meminfo_rejects_garbage() {
    let err = parse_meminfo(b"nonsense\n").expect_err("no MemTotal");
    assert_eq!(err.to_string(), "meminfo: no MemTotal");
}

fn linux_opts<'a>(fs: &'a MapFs, clock: &'a FakeClock, source: &'a FixedMemory, page_size: u64) -> Options<'a> {
    Options { os: Kind::Linux, fs, clock, source, proc_root: String::new(), page_size }
}

// Go: TestCollectLinux.
#[test]
fn collects_linux() {
    let psi_quiet = psi_fixture("0.00", "0.00");
    let psi_memory = psi_fixture("2.50", "0.00");
    let fs = MapFs::new([
        ("/proc/meminfo", MEMINFO),
        ("/proc/vmstat", "nr_free_pages 1\npswpin 12\npswpout 34\n"),
        ("/proc/pressure/cpu", psi_quiet.as_str()),
        ("/proc/pressure/io", psi_quiet.as_str()),
        ("/proc/pressure/memory", psi_memory.as_str()),
    ]);
    let at: jiff::Timestamp = "2026-11-23T10:00:00Z".parse().expect("time");
    let clock = FakeClock::new(at);
    let source = FixedMemory { darwin: None, page_size: 16384 };
    let ms = collect(&linux_opts(&fs, &clock, &source, 4096)).expect("collect");
    assert_eq!((ms.swap_ins, ms.swap_outs), (12, 34));
    assert_eq!(ms.psi.map(|p| p.some.avg10), Some(2.5));
    assert_eq!(ms.pressure_level, PressureLevel::Normal);
    assert_eq!(ms.pressure_reason, "75.0% of memory available");
    assert_eq!((ms.collected_at, ms.page_size_bytes), (GoTime::utc(at), 4096));
    // Without an override the page size comes from the source.
    assert_eq!(collect(&linux_opts(&fs, &clock, &source, 0)).expect("collect").page_size_bytes, 16384);
}

// Go: TestCollectLinuxWithoutPSIOrVMStat.
#[test]
fn collects_linux_without_psi_or_vmstat() {
    let fs = MapFs::new([("/proc/meminfo", MEMINFO)]);
    let (clock, source) = (FakeClock::new(jiff::Timestamp::UNIX_EPOCH), FixedMemory::default());
    let ms = collect(&linux_opts(&fs, &clock, &source, 0)).expect("collect");
    assert_eq!((ms.psi, ms.pressure_level), (None, PressureLevel::Normal));
}

// Go: TestCollectLinuxMissingMeminfo.
#[test]
fn collect_linux_needs_meminfo() {
    let fs = MapFs::default();
    let (clock, source) = (FakeClock::new(jiff::Timestamp::UNIX_EPOCH), FixedMemory::default());
    let err = collect(&linux_opts(&fs, &clock, &source, 0)).expect_err("no meminfo");
    assert_eq!(err.to_string(), "read meminfo: open /proc/meminfo: no such file or directory");
}

// Go: TestLinuxPressure.
#[test]
fn linux_pressure_table() {
    let line = |some: f64, full: f64| MemoryState {
        pressure_free_percent: 50.0,
        psi: Some(Resource { some: Line { avg10: some, ..Line::default() }, full: Some(Line { avg10: full, ..Line::default() }) }),
        ..MemoryState::default()
    };
    let avail = |pct: f64| MemoryState { pressure_free_percent: pct, ..MemoryState::default() };
    for (name, ms, want) in [
        ("plenty available, no stalls", line(0.0, 0.0), PressureLevel::Normal),
        ("some stall", line(12.0, 0.0), PressureLevel::Warning),
        ("brief full stall", line(5.0, 2.0), PressureLevel::Warning),
        ("sustained full stall", line(40.0, 15.0), PressureLevel::Critical),
        ("low available, no PSI", avail(10.0), PressureLevel::Warning),
        ("almost none available", avail(3.0), PressureLevel::Critical),
    ] {
        assert_eq!(linux::linux_pressure(&ms).0, want, "{name}");
    }
}

// Go: TestCollectDispatchesOnHostOS, with the OS chosen by the caller instead of the host.
#[test]
fn collect_dispatches_on_the_os() {
    let fs = MapFs::default();
    let clock = FakeClock::new(jiff::Timestamp::UNIX_EPOCH);
    let none = FixedMemory::default();
    let opts = |os, source| Options { os, fs: &fs, clock: &clock, source, proc_root: String::new(), page_size: 0 };
    assert!(matches!(collect(&opts(Kind::Darwin, &none)), Err(Error::NotSupported)));
    assert_eq!(Error::NotSupported.to_string(), "memory state collection not supported");
    let sample = FixedMemory { darwin: Some(DarwinSample { page_size_bytes: 16384, ..DarwinSample::default() }), page_size: 0 };
    let ms = collect(&opts(Kind::Darwin, &sample)).expect("darwin");
    assert_eq!((ms.page_size_bytes, ms.os.as_str(), ms.pressure_level), (16384, "", PressureLevel::Unknown));
    assert!(matches!(collect(&opts(Kind::Linux, &sample)), Err(Error::ReadMeminfo { .. })));
}

#[test]
fn errors_read_as_go() {
    assert_eq!(Error::VmStatistics(5).to_string(), "host_statistics64: kern_return=5");
    assert_eq!(Error::SwapUsage(io::Error::from_raw_os_error(1)).to_string(), "sysctl vm.swapusage: operation not permitted");
}

#[test]
fn json_matches_go_field_order_and_omitempty() {
    let ms = MemoryState { pressure_level: PressureLevel::Normal, ..MemoryState::default() };
    let out = String::from_utf8(gojson::to_vec(&ms).expect("json")).expect("utf8");
    assert_eq!(
        out,
        r#"{"physical_bytes":0,"page_size_bytes":0,"wired":0,"active":0,"inactive":0,"speculative":0,"free":0,"purgeable":0,"file_backed":0,"anonymous":0,"compressor_occupied":0,"compressor_stored":0,"compressor_ratio":0,"swap":{"total_bytes":0,"used_bytes":0,"free_bytes":0,"encrypted":false},"pressure_level":"Normal","pressure_free_percent":0,"compressions":0,"decompressions":0,"swap_ins":0,"swap_outs":0,"collected_at":"0001-01-01T00:00:00Z"}"#
    );
}

#[test]
fn json_reads_back_into_the_sample() {
    let psi = psi_fixture("2.50", "0.00");
    let fs = MapFs::new([
        ("/proc/meminfo", MEMINFO),
        ("/proc/pressure/cpu", psi.as_str()),
        ("/proc/pressure/io", psi.as_str()),
        ("/proc/pressure/memory", psi.as_str()),
    ]);
    let clock = FakeClock::new("2026-11-23T10:00:00.5Z".parse().expect("time"));
    let linux = collect(&linux_opts(&fs, &clock, &FixedMemory::default(), 4096)).expect("collect");
    let darwin = from_darwin_sample(
        &DarwinSample {
            page_size_bytes: 16384,
            physical_bytes: 1 << 34,
            pressure_level: Some(2),
            memorystatus_level: Some(41),
            ..DarwinSample::default()
        },
        GoTime::utc(jiff::Timestamp::UNIX_EPOCH),
    );
    for ms in [linux, darwin, MemoryState::default()] {
        let json = gojson::to_vec(&ms).expect("json");
        assert_eq!(serde_json::from_slice::<MemoryState>(&json).expect("reads back"), ms);
    }
}

#[test]
fn json_missing_fields_read_as_zero_and_an_unknown_level_as_unknown() {
    let ms: MemoryState = serde_json::from_str(r#"{"physical_bytes": 1024, "pressure_level": "Elevated", "extra": 1}"#).expect("json");
    assert_eq!((ms.physical_bytes, ms.pressure_level, ms.swap, ms.psi), (1024, PressureLevel::Unknown, SwapUsage::default(), None));
    assert_eq!(serde_json::from_str::<MemoryState>("{}").expect("empty"), MemoryState::default());
    let swap: MemoryState = serde_json::from_str(r#"{"swap": {"used_bytes": 1024}}"#).expect("partial swap");
    assert_eq!(swap.swap, SwapUsage { used_bytes: 1024, ..SwapUsage::default() });
    for level in [PressureLevel::Unset, PressureLevel::Unknown, PressureLevel::Normal, PressureLevel::Warning, PressureLevel::Critical] {
        assert_eq!(PressureLevel::from_name(level.as_str()), level);
    }
}

#[cfg(target_os = "macos")]
#[test]
fn host_memory_reads_this_mac() {
    let s = HostMemory.darwin().expect("live sample");
    assert!(s.physical_bytes > 0 && s.page_size_bytes >= 4096);
    assert!(s.vm.free + s.vm.active + s.vm.wire > 0);
    assert!(s.memorystatus_level.is_some_and(|l| l <= 100), "kern.memorystatus_level: {:?}", s.memorystatus_level);
    assert_eq!(HostMemory.page_size(), s.page_size_bytes);
}
