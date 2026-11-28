use super::*;

const SONOMA: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>elapsed_ns</key>
	<integer>1000000000</integer>
	<key>tasks</key>
	<array>
		<dict>
			<key>pid</key>
			<integer>101</integer>
			<key>name</key>
			<string>WindowServer</string>
			<key>energy_impact</key>
			<real>12.5</real>
			<key>cputime_ns</key>
			<integer>50000000</integer>
			<key>gputime_ns</key>
			<integer>2000000</integer>
			<key>ane_energy</key>
			<integer>0</integer>
			<key>short_timer_wakeups</key>
			<integer>3</integer>
			<key>qos_default_ms_per_s</key>
			<real>500.0</real>
			<key>qos_background_ms_per_s</key>
			<real>20.0</real>
		</dict>
		<dict>
			<key>pid</key>
			<integer>412</integer>
			<key>name</key>
			<string>kernel_task</string>
			<key>energy_impact</key>
			<real>4.2</real>
			<key>cputime_ns</key>
			<integer>10000000</integer>
		</dict>
	</array>
</dict>
</plist>
"#;

const SEQUOIA: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>elapsedNs</key>
	<integer>1000000000</integer>
	<key>tasks</key>
	<array>
		<dict>
			<key>pid</key>
			<integer>777</integer>
			<key>name</key>
			<string>com.apple.WebKit</string>
			<key>energyImpact</key>
			<real>2.0</real>
			<key>cputimeNs</key>
			<integer>30000000</integer>
			<key>gputimeNs</key>
			<integer>10000</integer>
			<key>aneEnergy</key>
			<integer>5000</integer>
			<key>shortTimerWakeups</key>
			<integer>11</integer>
			<key>qosDefaultMsPerS</key>
			<real>800.0</real>
			<key>qosBackgroundMsPerS</key>
			<real>0.0</real>
		</dict>
	</array>
</dict>
</plist>
"#;

// Go: TestParseTasksPlistSonoma.
#[test]
fn sonoma_keys() {
    let got = parse_tasks_plist(SONOMA.as_bytes()).expect("parse");
    assert_eq!(got.elapsed_ns, 1_000_000_000);
    assert_eq!(got.tasks.len(), 2);
    let first = &got.tasks[0];
    assert_eq!((first.pid, first.command.as_str()), (101, "WindowServer"));
    assert_eq!(first.energy_impact, 12.5);
    assert_eq!((first.cpu_ns, first.gpu_ns, first.short_timer_wakeups), (50_000_000, 2_000_000, 3));
    assert_eq!((first.qos_default_pct, first.qos_background_pct), (50.0, 2.0));
}

// Go: TestParseTasksPlistSequoiaKeys.
#[test]
fn sequoia_keys() {
    let got = parse_tasks_plist(SEQUOIA.as_bytes()).expect("parse");
    assert_eq!(got.tasks.len(), 1);
    let tk = &got.tasks[0];
    assert_eq!((tk.pid, tk.command.as_str()), (777, "com.apple.WebKit"));
    assert_eq!((tk.energy_impact, tk.cpu_ns, tk.ane_ns, tk.short_timer_wakeups), (2.0, 30_000_000, 5000, 11));
    assert_eq!(tk.qos_default_pct, 80.0);
}

// Go: TestParseTasksPlistEmpty.
#[test]
fn empty_input_is_an_error() {
    assert!(parse_tasks_plist(b"").is_err());
}

// Go: TestParseTasksPlistMalformedDictMissingValue.
#[test]
fn a_sparse_task_parses_with_zero_values() {
    let got =
        parse_tasks_plist(br"<plist><dict><key>tasks</key><array><dict><key>pid</key><integer>9</integer></dict></array></dict></plist>")
            .expect("parse");
    assert_eq!(got.tasks.len(), 1);
    assert_eq!(got.tasks[0].pid, 9);
    assert_eq!(got.tasks[0].energy_impact, 0.0);
}

// Go: TestPowermetricsTasksTopTasks.
#[test]
fn top_tasks_is_descending_and_does_not_mutate() {
    let p = PowermetricsTasks {
        elapsed_ns: 0,
        tasks: [(1, 1.0), (2, 5.0), (3, 3.0)]
            .into_iter()
            .map(|(pid, e)| TaskPowerSample { pid, energy_impact: e, ..TaskPowerSample::default() })
            .collect(),
    };
    let top = p.top_tasks(2);
    assert_eq!(top.iter().map(|t| t.pid).collect::<Vec<_>>(), [2, 3]);
    assert_eq!(p.tasks[0].pid, 1, "TopTasks must not mutate the underlying slice");
    assert!(p.top_tasks(0).is_empty() && p.top_tasks(-1).is_empty());
    assert_eq!(p.top_tasks(10).len(), 3);
}

#[test]
fn the_decoder_is_forgiving_like_go() {
    // Self-closing values, comments, entities, unknown tags, stray values, a bad integer, a
    // string holding a number, and a pid outside int32.
    let plist = r#"<plist><!-- c --><dict>
        <key>elapsed_ns</key><string> 42 </string>
        <key>tasks</key><array>
          <string>not a dict</string>
          <dict><key>pid</key><integer>-5</integer><key>name</key><string>a &amp; b &lt;x&gt; &#65; &bogus;</string><key>flag</key><true/>
                <key>energy_impact</key><integer>7</integer><key>data</key><data>AAEC</data></dict>
          <dict><key>pid</key><integer>2147483648</integer><key>command</key><string>c</string><string>stray</string>
                <key>cputime_ns</key><integer>x</integer><key>gputime_ns</key><real>3.9</real></dict>
          <dict><key>pid</key><integer>2147483647</integer><key>name</key><string/></dict>
        </array></dict></plist>"#;
    let got = parse_tasks_plist(plist.as_bytes()).expect("parse");
    assert_eq!(got.elapsed_ns, 42);
    assert_eq!(got.tasks.len(), 3);
    assert_eq!((got.tasks[0].pid, got.tasks[0].command.as_str(), got.tasks[0].energy_impact), (0, "a & b <x> A &bogus;", 7.0));
    assert_eq!((got.tasks[1].pid, got.tasks[1].command.as_str(), got.tasks[1].cpu_ns, got.tasks[1].gpu_ns), (0, "c", 0, 3));
    assert_eq!((got.tasks[2].pid, got.tasks[2].command.as_str()), (i64::from(i32::MAX), ""));
}

#[test]
fn a_truncated_plist_is_an_error() {
    assert!(parse_tasks_plist(b"<plist><dict><key>tasks</key><array>").is_err());
    assert_eq!(parse_tasks_plist(b"<plist></plist>").expect_err("no dict").to_string(), "plist: no root dict");
}

#[test]
fn tasks_serialize_null_when_there_are_none() {
    let json = String::from_utf8(spectra_base::gojson::to_vec(&PowermetricsTasks::default()).expect("json")).expect("utf8");
    assert_eq!(json, r#"{"elapsed_ns":0,"tasks":null}"#);
}
