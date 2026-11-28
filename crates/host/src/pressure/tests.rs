//! Ported from Go's `internal/pressure/pressure_test.go`.

use std::collections::BTreeMap;

use super::*;
use crate::testfs::MapFs;

const PSI_CPU: &str = "some avg10=4.76 avg60=26.31 avg300=10.34 total=36510539\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n";
const PSI_MEMORY: &str = "some avg10=12.50 avg60=3.00 avg300=1.00 total=100\nfull avg10=2.25 avg60=0.50 avg300=0.10 total=50\n";
const PSI_IO: &str = "some avg10=0.12 avg60=0.30 avg300=0.15 total=793942\nfull avg10=0.10 avg60=0.18 avg300=0.08 total=496093\n";

// Go: TestReadPSI.
#[test]
fn reads_host_psi_and_assesses_it() {
    let fs = MapFs::new([("/proc/pressure/cpu", PSI_CPU), ("/proc/pressure/memory", PSI_MEMORY), ("/proc/pressure/io", PSI_IO)]);
    let p = read_psi(&fs, "/proc").expect("psi");
    assert_eq!((p.cpu.some.avg60, p.cpu.some.total_usec), (26.31, 36510539));
    assert_eq!(p.memory.full.map(|f| f.avg10), Some(2.25));
    let kinds: BTreeMap<String, Severity> = assess_psi(&p).into_iter().map(|f| (f.kind, f.severity)).collect();
    assert_eq!(
        kinds,
        BTreeMap::from([("memory-pressure".to_string(), Severity::Warning), ("memory-stall".to_string(), Severity::Warning)]),
        "only memory findings (cpu some 4.76 < 10, io idle)"
    );
}

// Go: TestReadPSIUnavailable.
#[test]
fn missing_psi_is_unavailable() {
    let err = read_psi(&MapFs::default(), "/proc").expect_err("no psi");
    assert!(err.is_unavailable());
    assert_eq!(err.to_string(), "psi cpu: pressure data unavailable");
}

// Go: TestReadPSIOldCPUHasNoFull.
#[test]
fn old_cpu_psi_has_no_full_line() {
    let r = parse_resource(b"some avg10=1.00 avg60=2.00 avg300=3.00 total=7\n").expect("parse");
    assert_eq!((r.full, r.some.avg300), (None, 3.0));
}

// Go: TestParseResourceRejectsGarbage.
#[test]
fn parse_resource_rejects_garbage() {
    assert_eq!(
        parse_resource(b"some avg10=abc\n").expect_err("abc"),
        r#"parse avg10=abc: strconv.ParseFloat: parsing "abc": invalid syntax"#
    );
    assert_eq!(parse_resource(b"").expect_err("empty"), "no 'some' line");
    let fs = MapFs::new([("/proc/pressure/cpu", "some total=x\n")]);
    let err = read_psi(&fs, "/proc").expect_err("bad total");
    assert!(!err.is_unavailable());
    assert_eq!(err.to_string(), r#"psi cpu: parse total=x: strconv.ParseUint: parsing "x": invalid syntax"#);
}

// Go: TestV2Path.
#[test]
fn v2_path_table() {
    for (name, input, want) in [
        ("v2", "0::/system.slice/app.service\n", Some("/system.slice/app.service")),
        ("hybrid", "12:pids:/\n1:name=systemd:/x\n0::/user.slice\n", Some("/user.slice")),
        ("v1 only", "4:memory:/a\n3:cpu:/b\n", None),
        ("deleted", "0::/gone (deleted)\n", Some("/gone")),
        ("root", "0::/\n", Some("/")),
        ("relative", "0::a/../b\n", Some("/b")),
    ] {
        assert_eq!(v2_path(input.as_bytes()).as_deref(), want, "{name}");
    }
}

fn cg_fixture() -> MapFs {
    MapFs::new([
        ("/proc/42/cgroup", "0::/svc.slice/app.service\n"),
        ("/cg/cgroup.controllers", "cpu memory pids\n"),
        // The limit lives on the parent service slice, not the leaf.
        ("/cg/svc.slice/memory.max", "1073741824\n"),
        ("/cg/svc.slice/app.service/cgroup.controllers", "cpu memory pids\n"),
        ("/cg/svc.slice/app.service/memory.max", "max\n"),
        ("/cg/svc.slice/app.service/memory.high", "max\n"),
        ("/cg/svc.slice/app.service/memory.current", "1048576000\n"),
        ("/cg/svc.slice/app.service/memory.swap.current", "0\n"),
        ("/cg/svc.slice/app.service/memory.stat", "anon 1000000000\nfile 48576000\n"),
        ("/cg/svc.slice/app.service/memory.events", "low 0\nhigh 0\nmax 12\noom 3\noom_kill 2\n"),
        (
            "/cg/svc.slice/app.service/memory.pressure",
            "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
        ),
        (
            "/cg/svc.slice/app.service/cpu.stat",
            "usage_usec 5000000\nuser_usec 4000000\nsystem_usec 1000000\nnr_periods 100\nnr_throttled 40\nthrottled_usec 2500000\n",
        ),
        ("/cg/svc.slice/app.service/cpu.max", "50000 100000\n"),
        ("/cg/svc.slice/app.service/pids.current", "95\n"),
        ("/cg/svc.slice/app.service/pids.max", "100\n"),
    ])
}

// Go: TestReadCgroupAndAssess.
#[test]
fn reads_a_cgroup_and_assesses_it() {
    let cg = read_cgroup(&cg_fixture(), "/proc", "/cg", 42).expect("cgroup");
    let m = cg.memory.as_ref().expect("memory");
    assert_eq!((m.max, m.effective_max, m.limited_by.as_str()), (UNLIMITED, 1073741824, "/svc.slice"));
    assert_eq!((m.events.oom_kill, m.anon), (2, 1000000000));
    let c = cg.cpu.as_ref().expect("cpu");
    assert_eq!((c.quota_usec, c.period_usec, c.nr_throttled), (50000, 100000, 40));
    let findings = assess_cgroup(&cg);
    let got: BTreeMap<&str, Severity> = findings.iter().map(|f| (f.kind.as_str(), f.severity)).collect();
    for (kind, sev) in [
        ("cgroup-oom-kill", Severity::Critical),
        ("cgroup-memory-near-limit", Severity::Critical),
        ("cpu-throttled", Severity::Warning),
        ("pids-near-limit", Severity::Critical),
    ] {
        assert_eq!(got.get(kind), Some(&sev), "{kind} (all: {got:?})");
    }
    assert!(!got.contains_key("cgroup-memory-max-hit"), "max-hit must be suppressed once a kill is reported");
    let sums: Vec<&str> = findings.iter().map(|f| f.summary.as_str()).collect();
    assert!(sums.join("\n").contains("limit set on /svc.slice"), "near-limit summary should name the limiting ancestor: {sums:?}");
}

// Go: TestReadCgroupV1Unavailable.
#[test]
fn cgroup_v1_is_unavailable() {
    let fs = MapFs::new([("/proc/7/cgroup", "4:memory:/a\n"), ("/proc/8/cgroup", "0::/x\n")]);
    let err = read_cgroup(&fs, "/proc", "/cg", 7).expect_err("v1");
    assert!(err.is_unavailable());
    assert_eq!(err.to_string(), "pid 7 has no cgroup v2 path: pressure data unavailable");
    let err = read_cgroup(&fs, "/proc", "/cg", 8).expect_err("hybrid with no controllers");
    assert!(err.is_unavailable());
    assert_eq!(err.to_string(), "/cg/x is not a cgroup v2 directory: pressure data unavailable");
    let err = read_cgroup(&fs, "/proc", "/cg", 9).expect_err("no such pid");
    assert!(!err.is_unavailable());
    assert_eq!(err.to_string(), "read cgroup of pid 9: open /proc/9/cgroup: no such file or directory");
}

// Go: TestAssessHealthyCgroup.
#[test]
fn healthy_cgroup_has_no_findings() {
    let cg = Cgroup {
        path: "/a".into(),
        memory: Some(Memory { current: 100, anon: 50, effective_max: 1 << 30, max: UNLIMITED, high: UNLIMITED, ..Memory::default() }),
        ..Cgroup::default()
    };
    assert_eq!(assess_cgroup(&cg), vec![]);
}

#[test]
fn go_parse_quirks_are_kept() {
    let fs = MapFs::new([
        ("/cg/a/cgroup.controllers", ""),
        ("/cg/a/memory.current", "5\n"),
        // ParseUint's clamped value on a range error is still stored, as in Go.
        ("/cg/a/memory.swap.current", "99999999999999999999\n"),
        ("/cg/a/cpu.stat", "nr_periods 0\n"),
        ("/cg/a/cpu.max", "max 99999999999999999999\n"),
        ("/cg/a/pids.current", "3\n"),
        ("/cg/a/pids.max", "max\n"),
    ]);
    let cg = read_cgroup_path(&fs, "/cg", "/a").expect("cgroup");
    let m = cg.memory.expect("memory");
    assert_eq!((m.swap_current, m.max, m.effective_max, m.limited_by.as_str()), (u64::MAX, UNLIMITED, UNLIMITED, ""));
    let c = cg.cpu.expect("cpu");
    assert_eq!((c.quota_usec, c.period_usec), (UNLIMITED, i64::MAX));
    assert_eq!(cg.pids, Some(Pids { current: 3, max: UNLIMITED }));
    assert_eq!(cg.io_pressure, None);
}

#[test]
fn json_field_names_and_omitempty_match_go() {
    let r = Resource { some: Line { avg10: 1.5, ..Line::default() }, full: None };
    let out = spectra_base::gojson::to_vec(&r).expect("json");
    assert_eq!(String::from_utf8(out).expect("utf8"), r#"{"some":{"avg10":1.5,"avg60":0,"avg300":0,"total_usec":0}}"#);
    let cg = Cgroup { path: "/x".into(), pids: Some(Pids { current: 1, max: -1 }), ..Cgroup::default() };
    let out = spectra_base::gojson::to_vec(&cg).expect("json");
    assert_eq!(String::from_utf8(out).expect("utf8"), r#"{"path":"/x","pids":{"current":1,"max":-1}}"#);
}
