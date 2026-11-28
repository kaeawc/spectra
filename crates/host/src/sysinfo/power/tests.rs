use std::time::{Duration, Instant};

use spectra_base::fs::OsFs;
use spectra_base::proc::{FakeRunner, match_exact};

use super::*;
use crate::testfs::MapFs;

const BATT_ON_BATTERY: &str =
    "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=4718601)\t85%; discharging; 3:42 remaining present: true\n";
const BATT_ON_AC: &str = "Now drawing from 'AC Power'\n -InternalBattery-0 (id=4718601)\t100%; charged; 0:00 remaining present: true\n";
const THERM_NOMINAL: &str = "System thermal state: nominal\nCPU_Speed_Limit\t= 100\n";
const THERM_SERIOUS: &str = "System thermal state: serious\nCPU_Speed_Limit\t= 60\n";
const THERM_LOG_THROTTLED: &str = "2021-07-07 12:32:50 -0400 CPU Power notify
\tCPU_Scheduler_Limit \t= 100
\tCPU_Available_CPUs \t= 8
\tCPU_Speed_Limit \t= 90
2021-07-07 12:32:51 -0400 CPU Power notify
\tCPU_Scheduler_Limit \t= 100
\tCPU_Available_CPUs \t= 8
\tCPU_Speed_Limit \t= 92

2021-07-07 12:32:52 -0400 CPU Power notify
\tCPU_Scheduler_Limit \t= 100
\tCPU_Available_CPUs \t= 8
\tCPU_Speed_Limit \t= 100
";
const THERM_LOG_WITH_GARBAGE: &str = "2021-07-07 12:32:50 -0400 CPU Power notify
\tCPU_Scheduler_Limit \t= 100
\tCPU_Available_CPUs \t= 8
\tCPU_Speed_Limit \t= 90
2021-07-07 12:32:51 -0400 not a complete record
\tCPU_Speed_Limit \t= nope
2021-07-07 12:32:52 -0400 CPU Power notify
\tCPU_Available_CPUs \t= 8
\tCPU_Speed_Limit \t= 100
";
const ASSERTIONS_OUTPUT: &str = "Assertion status system-wide:
   BackgroundTask                 0
   PreventUserIdleSleep           1
   PreventUserIdleDisplaySleep    0
   PreventSystemSleep             0

Listed by owning process:
 pid 412(Slack): [0x000165b8000002f1] 00:00:04 PreventUserIdleSleep named: \"playing audio\"
";
const TOP_ENERGY_OUTPUT: &str = "PID    POWER  COMMAND\n99647  12.5   Slack\n412    3.2    com.apple.WebKit\n1      0.0    launchd\n";
const TOP_ENERGY_OUTPUT_WITH_SPACES: &str = "PID    POWER  COMMAND\n501    4.2    Google Chrome Helper\n";

/// The pmset and top answers of one stub host; an empty one fails.
fn stub_power(batt: &str, therm: &str, assertions: &str, top: &str) -> FakeRunner {
    let fake = FakeRunner::new();
    let rules: [(&str, &[&str], &str); 4] = [
        ("pmset", &["-g", "batt"], batt),
        ("pmset", &["-g", "therm"], therm),
        ("pmset", &["-g", "assertions"], assertions),
        ("top", &["-l", "2", "-s", "1", "-n", "10", "-o", "power", "-stats", "pid,power,command"], top),
    ];
    for (name, args, out) in rules {
        if out.is_empty() {
            fake.on_error(match_exact(name, args), "no output");
        } else {
            fake.on_stdout(match_exact(name, args), out);
        }
    }
    fake
}

fn collect_stub(batt: &str, therm: &str, assertions: &str, top: &str) -> PowerState {
    collect_power(Kind::Darwin, &stub_power(batt, therm, assertions, top), &MapFs::default())
}

// Go: TestCollectPowerOnBattery.
#[test]
fn on_battery() {
    let ps = collect_stub(BATT_ON_BATTERY, THERM_NOMINAL, "", "");
    assert!(ps.on_battery);
    assert_eq!(ps.battery_pct, 85);
    assert_eq!(ps.thermal_pressure, "nominal");
}

// Go: TestCollectPowerOnAC.
#[test]
fn on_ac() {
    let ps = collect_stub(BATT_ON_AC, THERM_NOMINAL, "", "");
    assert!(!ps.on_battery);
    assert_eq!(ps.battery_pct, 100);
}

// Go: TestCollectPowerThermal.
#[test]
fn thermal_serious_is_throttled() {
    let ps = collect_stub("", THERM_SERIOUS, "", "");
    assert_eq!(ps.thermal_pressure, "serious");
    assert!(ps.thermal_throttled);
    assert_eq!(ps.cpu_speed_limit_pct, 60);
    assert_eq!(ps.percent_thermal_throttled, 100);
}

// Go: TestCollectPowerAssertions.
#[test]
fn assertions_from_pmset() {
    let ps = collect_stub("", "", ASSERTIONS_OUTPUT, "");
    assert_eq!(ps.assertions, [PowerAssertion { kind: "PreventUserIdleSleep".into(), pid: 412, name: "playing audio".into() }]);
}

// Go: TestCollectAssertionsOnly.
#[test]
fn assertions_only_runs_one_command() {
    let runner = FakeRunner::new();
    runner.on_stdout(match_exact("pmset", &["-g", "assertions"]), ASSERTIONS_OUTPUT);
    let got = collect_assertions(&runner);
    assert_eq!(runner.calls().len(), 1, "assertions-only must not invoke batt/therm/top");
    assert_eq!(got.len(), 1);
    assert_eq!(got[0].pid, 412);
}

// Go: TestCollectAssertionsAbsorbsError.
#[test]
fn assertions_absorb_an_error() {
    assert!(collect_assertions(&FakeRunner::new()).is_empty());
}

// Go: TestCollectPowerAllFail.
#[test]
fn all_failing_is_the_zero_state() {
    assert_eq!(collect_power(Kind::Darwin, &FakeRunner::new(), &MapFs::default()), PowerState::default());
}

// Go: TestParseEnergyTop.
#[test]
fn parse_energy_top_rows() {
    let users = parse_energy_top(TOP_ENERGY_OUTPUT);
    assert_eq!(users.len(), 3);
    assert_eq!((users[0].pid, users[0].energy_impact, users[0].command.as_str()), (99647, 12.5, "Slack"));
    assert_eq!(users[2].energy_impact, 0.0);
}

// Go: TestParseEnergyTopKeepsLastSampleBlock.
#[test]
fn parse_energy_top_keeps_the_last_sample_block() {
    let two = "PID    POWER  COMMAND\n99647  0.0    Slack\n412    0.0    com.apple.WebKit\nPID    POWER  COMMAND\n99647  17.4   Slack\n412    4.1    com.apple.WebKit\n";
    let users = parse_energy_top(two);
    assert_eq!(users.len(), 2, "only the last block");
    assert_eq!(users[0].energy_impact, 17.4);
}

// Go: TestParseEnergyTopPreservesCommandWithSpaces.
#[test]
fn parse_energy_top_keeps_commands_with_spaces() {
    let users = parse_energy_top(TOP_ENERGY_OUTPUT_WITH_SPACES);
    assert_eq!(users.len(), 1);
    assert_eq!(users[0].command, "Google Chrome Helper");
}

#[test]
fn parse_energy_top_skips_rows_that_do_not_parse() {
    let out = "PID POWER COMMAND\nabc 1.0 x\n7 notanumber y\n8 1e999 z\n9 2.5 ok\n";
    let users = parse_energy_top(out);
    assert_eq!(users.iter().map(|u| u.pid).collect::<Vec<_>>(), [9]);
}

// Go: TestParseThermAggregatesSpeedLimits.
#[test]
fn therm_aggregates_speed_limits() {
    let mut ps = PowerState::default();
    parse_therm(THERM_LOG_THROTTLED, &mut ps);
    assert!(ps.thermal_throttled);
    assert_eq!(ps.cpu_speed_limit_pct, 100, "latest");
    assert_eq!(ps.lowest_cpu_speed_limit_pct, 90);
    assert_eq!(ps.average_cpu_speed_limit_pct, 94);
    assert_eq!(ps.percent_thermal_throttled, 66);
    assert_eq!(ps.cpu_speed_limit_samples, [90, 92, 100]);
}

// Go: TestParseThermSkipsInvalidSpeedLimits.
#[test]
fn therm_skips_invalid_speed_limits() {
    let mut ps = PowerState::default();
    parse_therm(THERM_LOG_WITH_GARBAGE, &mut ps);
    assert_eq!(ps.cpu_speed_limit_samples.len(), 2, "two valid samples");
    assert_eq!(ps.lowest_cpu_speed_limit_pct, 90);
}

#[test]
fn therm_accepts_the_short_state_prefix() {
    let mut ps = PowerState::default();
    parse_therm("  thermal state: fair\n", &mut ps);
    assert_eq!(ps.thermal_pressure, "fair");
}

// Go: TestCollectPowerEnergyTopUsers.
#[test]
fn energy_top_users() {
    let ps = collect_stub("", "", "", TOP_ENERGY_OUTPUT);
    assert_eq!(ps.energy_top_users.len(), 3);
    assert_eq!(ps.energy_top_users[1].pid, 412);
}

/// A source whose probes each take `delay`, to observe the fan-out.
struct Slow {
    delay: Duration,
    batt: &'static str,
    therm: &'static str,
    assertions: &'static str,
    top: &'static str,
}

impl Slow {
    fn probe(&self, out: &'static str) -> Result<Vec<u8>, String> {
        #[allow(clippy::disallowed_methods)] // measuring real wall-clock concurrency
        std::thread::sleep(self.delay);
        if out.is_empty() { Err("no output".into()) } else { Ok(out.as_bytes().to_vec()) }
    }
}

impl PowerSource for Slow {
    fn battery(&self) -> Result<Vec<u8>, String> {
        self.probe(self.batt)
    }

    fn thermal(&self) -> Result<Vec<u8>, String> {
        self.probe(self.therm)
    }

    fn assertions(&self) -> Result<Vec<u8>, String> {
        self.probe(self.assertions)
    }

    fn energy_top(&self) -> Result<Vec<u8>, String> {
        self.probe(self.top)
    }
}

fn slow(delay: Duration) -> Slow {
    Slow { delay, batt: BATT_ON_BATTERY, therm: THERM_NOMINAL, assertions: ASSERTIONS_OUTPUT, top: TOP_ENERGY_OUTPUT }
}

// Go: TestCollectPowerRunsProbesConcurrently.
#[test]
fn probes_run_concurrently() {
    let delay = Duration::from_millis(150);
    #[allow(clippy::disallowed_methods)] // measuring real wall-clock concurrency
    let start = Instant::now();
    let ps = collect(&slow(delay));
    #[allow(clippy::disallowed_methods)]
    let elapsed = start.elapsed();
    // Serial would be 4 delays; generous slack for a slow CI.
    assert!(elapsed < 3 * delay, "collect took {elapsed:?} with 4 probes at {delay:?} each; expected concurrent execution");
    assert!(ps.on_battery);
    assert_eq!(ps.thermal_pressure, "nominal");
    assert_eq!(ps.assertions.len(), 1);
    assert_eq!(ps.energy_top_users.len(), 3);
}

// Go: TestCollectPowerPartialFailure.
#[test]
fn a_failed_probe_leaves_its_fields_zero() {
    let src = Slow { therm: "", ..slow(Duration::ZERO) };
    let ps = collect(&src);
    assert!(ps.on_battery);
    assert_eq!(ps.thermal_pressure, "");
    assert_eq!(ps.assertions.len(), 1);
    assert_eq!(ps.energy_top_users.len(), 3);
}

// Go: TestPowerCollectorWithFakeSource.
#[test]
fn collector_with_a_fake_source() {
    let src = Slow { therm: THERM_SERIOUS, top: TOP_ENERGY_OUTPUT_WITH_SPACES, ..slow(Duration::ZERO) };
    let ps = collect(&src);
    assert!(ps.on_battery);
    assert_eq!(ps.thermal_pressure, "serious");
    assert_eq!(ps.assertions.len(), 1);
    assert_eq!(ps.energy_top_users.len(), 1);
    assert_eq!(ps.energy_top_users[0].command, "Google Chrome Helper");
}

// Go: TestParseBattHasBattery.
#[test]
fn parse_batt_reports_has_battery() {
    let mut laptop = PowerState::default();
    parse_batt(
        "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=4653155)\t85%; discharging; 4:12 remaining present: true\n",
        &mut laptop,
    );
    assert!(laptop.has_battery == Some(true) && laptop.on_battery && laptop.battery_pct == 85, "{laptop:?}");
    let mut desktop = PowerState::default();
    parse_batt("Now drawing from 'AC Power'\n", &mut desktop);
    assert!(desktop.has_battery == Some(false) && !desktop.on_battery, "{desktop:?}");
}

#[test]
fn percent_is_the_first_digit_run_before_a_percent_sign() {
    assert_eq!(extract_pct(" 12 slots 34%; x 56%"), Some(34));
    assert_eq!(extract_pct("100%"), Some(100));
    assert_eq!(extract_pct("no percent 5"), None);
    assert_eq!(extract_pct("%"), None);
}

#[test]
fn assertion_lines_follow_the_regexes() {
    let line = r#" pid 99(Some App): [0x1] 1:02 NoIdleSleepAssertion named: "a b" "#;
    assert_eq!(assertion_pid(line), Some(99));
    assert_eq!(assertion_type(line), Some("NoIdleSleepAssertion"));
    assert_eq!(assertion_name(line), Some("a b"));
    assert_eq!(assertion_pid("pid x(1)"), None);
    assert_eq!(assertion_type("] 12:00"), None);
    assert_eq!(assertion_name("named: nope"), None);
    assert_eq!(assertion_name("named:\"x\""), None, "\\s+ needs at least one space");
    // No `named:` still yields the assertion, with an empty name.
    let parsed = parse_assertions(" pid 5(X): [0x2] 00:00:01 PreventSystemSleep\nnot a row\n");
    assert_eq!(parsed, [PowerAssertion { kind: "PreventSystemSleep".into(), pid: 5, name: String::new() }]);
}

// Go: TestCollectPowerLinuxOnBattery.
#[test]
fn linux_on_battery() {
    let fs = MapFs::new([
        ("/sys/class/power_supply/AC/type", "Mains\n"),
        ("/sys/class/power_supply/AC/online", "0\n"),
        ("/sys/class/power_supply/BAT0/type", "Battery\n"),
        ("/sys/class/power_supply/BAT0/capacity", "72\n"),
        ("/sys/class/power_supply/BAT0/status", "Discharging\n"),
    ]);
    let ps = collect_power(Kind::Linux, &FakeRunner::new(), &fs);
    assert!(ps.on_battery, "OnBattery should be true when discharging and AC offline");
    assert_eq!(ps.battery_pct, 72);
}

// Go: TestCollectPowerLinuxOnAC.
#[test]
fn linux_on_ac() {
    let fs = MapFs::new([
        ("/sys/class/power_supply/AC/type", "Mains\n"),
        ("/sys/class/power_supply/AC/online", "1\n"),
        ("/sys/class/power_supply/BAT0/type", "Battery\n"),
        ("/sys/class/power_supply/BAT0/capacity", "100\n"),
        ("/sys/class/power_supply/BAT0/status", "Full\n"),
    ]);
    let ps = collect_power_linux(&fs, "/sys/class/power_supply");
    assert!(!ps.on_battery, "OnBattery should be false when AC is online");
    assert_eq!(ps.battery_pct, 100);
}

#[test]
fn linux_battery_without_an_ac_supply_is_on_battery_only_when_it_says_so() {
    // AC seen but offline: on battery even when the status is not "Discharging".
    let fs =
        MapFs::new([("/p/AC/type", "USB\n"), ("/p/AC/online", "0\n"), ("/p/BAT0/type", "Battery\n"), ("/p/BAT0/status", "Not charging\n")]);
    assert!(collect_power_linux(&fs, "/p").on_battery);
    // A battery and no AC supply at all: not on battery unless discharging.
    let only = MapFs::new([("/p/BAT0/type", "Battery\n"), ("/p/BAT0/status", "Full\n")]);
    assert!(!collect_power_linux(&only, "/p").on_battery);
}

// Go: TestCollectPowerLinuxMissingTree.
#[test]
fn linux_missing_tree_is_zero() {
    let ps = collect_power_linux(&MapFs::default(), "/sys/class/power_supply");
    assert!(!ps.on_battery && ps.battery_pct == 0);
    assert_eq!(ps.has_battery, None, "unreadable tree: unknown");
}

// Go: TestCollectPowerLinuxHasBattery.
#[test]
fn linux_has_battery() {
    let fs = MapFs::new([("/sys/class/power_supply/BAT0/type", "Battery\n")]);
    assert_eq!(collect_power_linux(&fs, "/sys/class/power_supply").has_battery, Some(true));
}

// Go: TestCollectPowerLinuxNoBattery: a cloud VM, whose class directory exists and is empty.
#[test]
fn linux_no_battery() {
    let dir = std::env::temp_dir().join(format!("spectra-power-{}", std::process::id()));
    std::fs::create_dir_all(&dir).expect("mkdir");
    let ps = collect_power_linux(&OsFs, dir.to_str().expect("utf8"));
    std::fs::remove_dir_all(&dir).expect("cleanup");
    assert_eq!(ps.has_battery, Some(false), "empty power_supply");
}

#[test]
fn power_state_omits_empty_fields_like_go() {
    let json = String::from_utf8(spectra_base::gojson::to_vec(&PowerState::default()).expect("json")).expect("utf8");
    assert_eq!(json, r#"{"on_battery":false,"battery_pct":0,"thermal_throttled":false}"#);
}
