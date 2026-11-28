use super::*;

const SEC: GoDuration = GoDuration(1_000_000_000);

fn input(pid: i64) -> ImpactInput {
    ImpactInput { pid, interval: SEC, ..ImpactInput::default() }
}

// Go: TestComputeImpactReproducible.
#[test]
fn compute_impact_is_reproducible_and_sums_its_parts() {
    let i = ImpactInput {
        pid: 91829,
        command: "Claude Helper (GPU)".into(),
        interval: SEC,
        billed_energy_j: 0.28,
        interrupt_wakeups: 9100,
        gpu_time_ns: 41_000_000,
        disk_bytes: 0,
    };
    let first = compute_impact(&i, &[], &DEFAULT_WEIGHTS);
    for n in 0..100 {
        assert_eq!(compute_impact(&i, &[], &DEFAULT_WEIGHTS), first, "iter {n} differs");
    }
    assert!(first.total > 0.0, "{first:?}");
    let sum = first.from_energy + first.from_wakeups + first.from_gpu + first.from_assertions + first.from_io;
    assert!((sum - first.total).abs() <= 1e-9, "total {} != sum of components {sum}", first.total);
}

// Go: TestComputeImpactCalibration: `yes > /dev/null` and `stress-ng --cpu 1 -t 10s` both
// saturate one core and score within 10% of each other.
#[test]
fn calibration_stays_within_ten_percent() {
    let ten = GoDuration(10_000_000_000);
    let yes = ImpactInput { pid: 1, interval: ten, billed_energy_j: 30.0, interrupt_wakeups: 100, ..ImpactInput::default() };
    let stress = ImpactInput { pid: 2, interval: ten, billed_energy_j: 31.5, interrupt_wakeups: 90, ..ImpactInput::default() };
    let a = compute_impact(&yes, &[], &DEFAULT_WEIGHTS).total;
    let b = compute_impact(&stress, &[], &DEFAULT_WEIGHTS).total;
    let diff = (a - b).abs() / a.max(b);
    assert!(diff <= 0.10, "calibration drift {:.2}% > 10% (yes={a} stress={b})", diff * 100.0);
}

fn assertion(pid: i64, kind: &str) -> PowerAssertion {
    PowerAssertion { kind: kind.into(), pid, name: String::new() }
}

// Go: TestComputeImpactAssertionOutranksIdle.
#[test]
fn an_idle_blocking_assertion_outranks_idle() {
    let assertions = [PowerAssertion { name: "caffeinate -i".into(), ..assertion(500, ASSERTION_PREVENT_USER_IDLE_SLEEP) }];
    let c = compute_impact(&input(500), &assertions, &DEFAULT_WEIGHTS);
    let i = compute_impact(&input(501), &assertions, &DEFAULT_WEIGHTS);
    assert!(c.total > i.total, "caffeinate ({}) should outrank idle ({})", c.total, i.total);
    assert_eq!(c.from_assertions, DEFAULT_WEIGHTS.assertion_penalty);
    assert_eq!(i.total, 0.0);
}

// Go: TestComputeImpactAssertionScopedToPID.
#[test]
fn an_assertion_held_by_another_pid_does_not_count() {
    let got = compute_impact(&input(10), &[assertion(99, ASSERTION_PREVENT_USER_IDLE_SLEEP)], &DEFAULT_WEIGHTS);
    assert_eq!(got.from_assertions, 0.0);
}

// Go: TestComputeImpactNonBlockingAssertionIgnored.
#[test]
fn a_non_blocking_assertion_is_ignored() {
    let got = compute_impact(&input(10), &[assertion(10, "BackgroundTask")], &DEFAULT_WEIGHTS);
    assert_eq!(got.from_assertions, 0.0);
}

#[test]
fn every_idle_blocking_assertion_adds_its_penalty() {
    let all = [
        ASSERTION_PREVENT_USER_IDLE_SYSTEM_SLEEP,
        ASSERTION_PREVENT_USER_IDLE_DISPLAY_SLEEP,
        ASSERTION_PREVENT_USER_IDLE_SLEEP,
        ASSERTION_PREVENT_SYSTEM_SLEEP,
        ASSERTION_NO_IDLE_SLEEP,
        ASSERTION_NO_DISPLAY_SLEEP,
        ASSERTION_USER_IS_ACTIVE,
    ];
    let held: Vec<PowerAssertion> = all.iter().map(|k| assertion(10, k)).collect();
    assert_eq!(compute_impact(&input(10), &held, &DEFAULT_WEIGHTS).from_assertions, 7.0 * DEFAULT_WEIGHTS.assertion_penalty);
}

// Go: TestComputeImpactZeroIntervalUsesRawCount.
#[test]
fn a_zero_interval_uses_the_raw_wakeup_count() {
    let i = ImpactInput { pid: 10, interrupt_wakeups: 1000, ..ImpactInput::default() };
    let got = compute_impact(&i, &[], &DEFAULT_WEIGHTS);
    assert!((got.from_wakeups - 1000.0 * DEFAULT_WEIGHTS.interrupt_wakeup_cost).abs() <= 1e-9);
}

// Go: TestFromRusageConvertsUnits.
#[test]
fn from_rusage_converts_units() {
    let d = EnergyDelta {
        pid: 42,
        command: "yes".into(),
        interval: SEC,
        billed_energy_nj: 3_000_000_000,
        interrupt_wakeups: 50,
        disk_bytes_read: 1000,
        disk_bytes_written: 500,
        ..EnergyDelta::default()
    };
    let got = from_rusage(&d);
    assert_eq!((got.pid, got.command.as_str(), got.interval), (42, "yes", SEC));
    assert_eq!(got.billed_energy_j, 3.0);
    assert_eq!(got.disk_bytes, 1500);
}

// Go: TestFromTaskSampleCarriesGPU.
#[test]
fn from_task_sample_carries_gpu() {
    let t = TaskPowerSample { pid: 7, command: "WindowServer".into(), gpu_ns: 12_000_000, ..TaskPowerSample::default() };
    let got = from_task_sample(&t, GoDuration(2_000_000_000));
    assert_eq!((got.pid, got.interval), (7, GoDuration(2_000_000_000)));
    assert_eq!(got.gpu_time_ns, 12_000_000);
}

// Go: TestParseWeightOverrides.
#[test]
fn weight_overrides_full() {
    let w = parse_weight_overrides("energy=120,wake=0.002,gpu=2e-7,assert=10,io=5e-9", &DEFAULT_WEIGHTS).expect("parse");
    assert_eq!(
        w,
        ImpactWeights {
            billed_energy_per_joule: 120.0,
            interrupt_wakeup_cost: 0.002,
            gpu_ns_cost: 2e-7,
            assertion_penalty: 10.0,
            disk_byte_cost: 5e-9
        }
    );
}

// Go: TestParseWeightOverridesPartial.
#[test]
fn weight_overrides_partial_keep_the_defaults() {
    let w = parse_weight_overrides("energy=200", &DEFAULT_WEIGHTS).expect("parse");
    assert_eq!(w.billed_energy_per_joule, 200.0);
    assert_eq!(w.interrupt_wakeup_cost, DEFAULT_WEIGHTS.interrupt_wakeup_cost);
}

// Go: TestParseWeightOverridesEmpty.
#[test]
fn weight_overrides_empty() {
    assert_eq!(parse_weight_overrides("", &DEFAULT_WEIGHTS), Ok(DEFAULT_WEIGHTS));
    assert_eq!(parse_weight_overrides(" , ,", &DEFAULT_WEIGHTS), Ok(DEFAULT_WEIGHTS));
}

// Go: TestParseWeightOverridesErrors.
#[test]
fn weight_override_errors() {
    assert_eq!(
        parse_weight_overrides("garbage=1.0", &DEFAULT_WEIGHTS),
        Err("impact weight \"garbage\": unknown key (want energy|wake|gpu|assert|io)".into())
    );
    assert_eq!(parse_weight_overrides("energy", &DEFAULT_WEIGHTS), Err("impact weight \"energy\": expected key=value".into()));
    assert_eq!(
        parse_weight_overrides("energy=banana", &DEFAULT_WEIGHTS),
        Err("impact weight \"energy\": strconv.ParseFloat: parsing \"banana\": invalid syntax".into())
    );
    assert_eq!(
        parse_weight_overrides("energy=1e999", &DEFAULT_WEIGHTS),
        Err("impact weight \"energy\": strconv.ParseFloat: parsing \"1e999\": value out of range".into())
    );
}

// Go: TestLoadWeightsFromEnv.
#[test]
fn load_weights_reads_the_injected_env() {
    let w = load_weights(|k| if k == "SPECTRA_IMPACT_WEIGHTS" { "energy=150".into() } else { String::new() }).expect("load");
    assert_eq!(w.billed_energy_per_joule, 150.0);
    assert_eq!(load_weights(|_| String::new()), Ok(DEFAULT_WEIGHTS));
}

// Go: TestImpactInputUnmarshalWakeupKeys.
#[test]
fn input_json_accepts_both_wakeup_keys() {
    let cases: &[(&str, &str, u64)] = &[
        ("count", r#"{"pid":1,"interval_ns":2000000000,"interrupt_wakeups":300}"#, 300),
        ("rate over interval", r#"{"pid":1,"interval_ns":2000000000,"interrupt_wakeups_per_sec":150}"#, 300),
        ("rate without interval", r#"{"pid":1,"interrupt_wakeups_per_sec":150}"#, 150),
        ("count wins over rate", r#"{"pid":1,"interrupt_wakeups":7,"interrupt_wakeups_per_sec":150}"#, 7),
        ("explicit zero count wins over rate", r#"{"pid":1,"interrupt_wakeups":0,"interrupt_wakeups_per_sec":5000}"#, 0),
    ];
    for (name, raw, want) in cases {
        let got = ImpactInput::from_json(raw.as_bytes()).expect(name);
        assert!(got.interrupt_wakeups == *want && got.pid == 1, "{name}: {got:?}");
    }
    let full = ImpactInput::from_json(
        br#"{"pid":3,"command":"x","interval_ns":5,"billed_energy_j":0.5,"gpu_time_ns":9,"disk_bytes":4,"extra":true}"#,
    )
    .expect("full");
    assert_eq!(
        full,
        ImpactInput {
            pid: 3,
            command: "x".into(),
            interval: GoDuration(5),
            billed_energy_j: 0.5,
            interrupt_wakeups: 0,
            gpu_time_ns: 9,
            disk_bytes: 4
        }
    );
    assert!(ImpactInput::from_json(b"[]").is_err());
}

// Go: TestImpactFormulaHelpNamesInputKey.
#[test]
fn help_names_the_input_key() {
    let help = impact_formula_help(&DEFAULT_WEIGHTS);
    assert!(!help.contains("interrupt_wakeups_per_sec") && help.contains("interrupt_wakeups"), "{help}");
}

// Go: TestImpactFormulaHelpMentionsWeights.
#[test]
fn help_mentions_the_weights() {
    let help = impact_formula_help(&DEFAULT_WEIGHTS);
    for want in ["score = energy", "billed_energy_J", "100", "SPECTRA_IMPACT_WEIGHTS"] {
        assert!(help.contains(want), "help missing {want:?}:\n{help}");
    }
}

#[test]
fn help_formats_weights_as_percent_g() {
    let help = impact_formula_help(&DEFAULT_WEIGHTS);
    assert!(help.contains("energy     = billed_energy_J            × 100  (per joule)\n"), "{help}");
    assert!(help.contains("wakeups    = interrupt_wakeups/interval × 0.001  (per wakeup/s)\n"), "{help}");
    assert!(help.contains("gpu        = gpu_time_ns                × 1e-07\n"), "{help}");
    assert!(help.contains("io         = disk_bytes                 × 1e-09\n"), "{help}");
    assert_eq!(format_g(1_234_567.0), "1.234567e+06");
    assert_eq!(format_g(123_456.0), "123456");
    assert_eq!(format_g(0.00012), "0.00012");
    assert_eq!(format_g(0.00001), "1e-05");
    assert_eq!(format_g(0.0), "0");
}

// Go: TestScoreImpactsSortedAndStable.
#[test]
fn scores_sort_by_total_then_pid() {
    let inputs: Vec<ImpactInput> =
        [(100, 0.1), (200, 0.5), (300, 0.1)].into_iter().map(|(pid, j)| ImpactInput { billed_energy_j: j, ..input(pid) }).collect();
    let got = score_impacts(&inputs, &[], &DEFAULT_WEIGHTS);
    assert_eq!(got.iter().map(|s| s.input.pid).collect::<Vec<_>>(), [200, 100, 300]);
}

#[test]
fn input_serializes_like_go() {
    let json = String::from_utf8(
        gojson::to_vec(&ImpactInput { pid: 4, interval: SEC, billed_energy_j: 0.25, ..ImpactInput::default() }).expect("json"),
    )
    .expect("utf8");
    assert_eq!(json, r#"{"pid":4,"interval_ns":1000000000,"billed_energy_j":0.25}"#);
}
