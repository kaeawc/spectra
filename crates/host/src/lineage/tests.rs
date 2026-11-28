use super::*;

fn p(pid: i64, ppid: i64, cmd: &str, rss: i64, cpu: f64) -> Info {
    Info {
        pid,
        ppid,
        command: cmd.into(),
        full_command_line: cmd.into(),
        rss_kib: rss,
        cpu_pct: cpu,
        uid: 501,
        user: "me".into(),
        vsize_kib: 100,
        ..Info::default()
    }
}

fn pids(a: &[Ancestor]) -> Vec<i64> {
    a.iter().map(|x| x.pid).collect()
}

// Go: TestAncestorChain.
#[test]
fn ancestor_chain() {
    let root = Info { uid: 0, ..p(1, 0, "launchd", 1, 0.0) };
    let cases: Vec<(&str, Vec<Info>, i64, Vec<i64>)> = vec![
        ("stops at pid 1", vec![root.clone(), p(10, 1, "zsh", 1, 0.0), p(20, 10, "make", 1, 0.0)], 20, vec![20, 10, 1]),
        ("pid 1 itself", vec![root.clone()], 1, vec![1]),
        ("missing parent", vec![p(20, 99, "x", 1, 0.0)], 20, vec![20]),
        ("self parent", vec![p(20, 20, "x", 1, 0.0)], 20, vec![20]),
        ("pid0 parent", vec![p(20, 0, "x", 1, 0.0), p(0, 0, "kernel", 1, 0.0)], 20, vec![20]),
        ("cycle", vec![p(2, 3, "a", 1, 0.0), p(3, 4, "b", 1, 0.0), p(4, 2, "c", 1, 0.0)], 2, vec![2, 3, 4]),
    ];
    for (name, procs, pid, want) in cases {
        assert_eq!(pids(&build(&procs, pid).expect(name).ancestors), want, "{name}");
    }
}

// Go: TestDescendantsAndTotals.
#[test]
fn descendants_and_totals() {
    let procs = [
        p(1, 0, "launchd", 1, 0.0),
        p(10, 1, "root", 100, 1.5),
        p(11, 10, "a", 200, 2.0),
        p(12, 10, "b", 300, 3.0),
        p(13, 11, "c", 400, 4.0),
        p(99, 1, "other", 9999, 9.0),
    ];
    let r = build(&procs, 10).expect("build");
    assert_eq!((r.tree.count, r.tree.subtree_rss_kib, r.tree.subtree_cpu_pct), (4, 1000, 10.5));
    assert_eq!(r.tree.children.len(), 2);
    assert_eq!((r.tree.children[0].pid, r.tree.children[0].children[0].pid), (11, 13));
    assert_eq!((r.tree.children[0].rss_kib, r.tree.children[0].subtree_rss_kib), (200, 600));
}

// Go: TestDescendantCycleTerminates.
#[test]
fn descendant_cycle_terminates() {
    assert_eq!(build(&[p(2, 3, "a", 1, 0.0), p(3, 2, "b", 1, 0.0)], 2).expect("build").tree.count, 2);
}

// Go: TestZombieFlag.
#[test]
fn zombie_flag() {
    let z = |ppid: i64, cmd: &str, full: &str, vsz: i64| Info {
        pid: 5,
        ppid,
        command: cmd.into(),
        full_command_line: full.into(),
        vsize_kib: vsz,
        ..Info::default()
    };
    for (name, info, want) in [
        ("macos live parens", z(10, "(bash)", "(bash)", 0), false),
        ("sd-pam", z(10, "(sd-pam)", "(sd-pam)", 0), false),
        ("defunct", z(10, "sh <defunct>", "", 0), true),
        ("macos bare defunct", z(10, "<defunct>", "<defunct>", 0), true),
        ("macos bare defunct with memory", z(10, "<defunct>", "<defunct>", 8), false),
        ("linux zombie", z(1234, "sh", "[sh]", 0), true),
        ("orphan zombie adopted by init", z(1, "sh", "[sh]", 0), true),
        ("live arg mentions defunct", z(10, "grep", "grep x <defunct>", 100), false),
        ("live arg mentions defunct no vsize", z(10, "grep", "grep -e foo <defunct>", 0), false),
        ("kernel thread ppid 2", z(2, "kworker/0:1", "[kworker/0:1]", 0), false),
        ("kernel thread ppid 0", z(0, "kthreadd", "[kthreadd]", 0), false),
        ("bracket with vsize", z(10, "sh", "[sh]", 5), false),
        ("normal", z(10, "sh", "sh -c (x)", 0), false),
    ] {
        assert_eq!(is_zombie(&info), want, "{name}");
    }
}

// Go: TestZombieFlagOnNode.
#[test]
fn zombie_flag_on_node() {
    let z = Info { pid: 11, ppid: 10, command: "sh <defunct>".into(), ..Info::default() };
    let r = build(&[p(10, 1, "x", 1, 0.0), z], 10).expect("build");
    assert_eq!(r.tree.children[0].flags, [FLAG_ZOMBIE]);
}

// Go: TestReparented.
#[test]
fn reparented_flag() {
    let root = Info { uid: 0, ..p(1, 0, "launchd", 1, 0.0) };
    let user = p(10, 1, "daemonized", 1, 0.0);
    let root_owned = Info { uid: 0, ..p(11, 1, "sshd", 1, 0.0) };
    let child = p(12, 10, "kid", 1, 0.0);
    let procs = [root, user, root_owned, child];
    assert_eq!(build(&procs, 10).expect("10").flags, [FLAG_REPARENTED]);
    assert!(build(&procs, 11).expect("11").flags.is_empty());
    assert!(build(&procs, 12).expect("12").flags.is_empty());
}

// Go: TestUnknownPID.
#[test]
fn unknown_pid() {
    let err = build(&[p(1, 0, "launchd", 1, 0.0)], 42).expect_err("42");
    assert_eq!((err, err.to_string()), (NotFound(42), "pid 42: no such process".to_string()));
}

// Go: TestJSONShape.
#[test]
fn json_shape() {
    let r = build(&[p(1, 0, "launchd", 1, 0.0), p(10, 1, "a", 5, 1.0), p(11, 10, "b", 7, 2.0)], 10).expect("build");
    let b = String::from_utf8(spectra_base::gojson::to_vec(&r).expect("json")).expect("utf8");
    let v: serde_json::Value = serde_json::from_str(&b).expect("parse");
    for k in ["pid", "ancestors", "tree"] {
        assert!(v.get(k).is_some(), "missing {k} in {b}");
    }
    for k in ["pid", "rss_kib", "cpu_pct", "count", "subtree_rss_kib", "subtree_cpu_pct", "children"] {
        assert!(v["tree"].get(k).is_some(), "tree missing {k} in {b}");
    }
    assert!(b.contains(r#""ancestors":[{"pid":10"#), "{b}");
}
