use super::*;

/// `lsof -p 412,999` (header plus representative rows for two pids).
const LSOF: &str = "COMMAND    PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
Slack      412   alice  cwd    DIR               1,15      512          222552 /Applications/Slack.app
Slack      412   alice  txt    REG               1,15  1234567          111111 /Applications/Slack.app/Contents/MacOS/Slack
Slack      412   alice    0r   CHR                3,2      0t0             339 /dev/null
Slack      412   alice    1w   REG               1,15        0          229784 /tmp/slack.log
Slack      412   alice    2w   REG               1,15        0          229784 /tmp/slack.log
Slack      412   alice   10u  IPv4 0xabc          0t0  TCP  *:3000 (LISTEN)
Slack      412   alice   29u  IPv4 0xabc          0t0  TCP  127.0.0.1:55123->127.0.0.1:443 (ESTABLISHED)
bash       999   alice  cwd    DIR               1,15      512          222552 /home/alice
bash       999   alice  txt    REG               1,15   987654          222222 /bin/bash
bash       999   alice    0u   CHR                3,2      0t0             339 /dev/tty
bash       999   alice    1u   CHR                3,2      0t0             339 /dev/tty
bash       999   alice    2u   CHR                3,2      0t0             339 /dev/tty
";

fn two() -> Vec<Info> {
    vec![Info { pid: 412, command: "Slack".into(), ..Info::default() }, Info { pid: 999, command: "bash".into(), ..Info::default() }]
}

fn parsed() -> Vec<Info> {
    let mut procs = two();
    parse_lsof_deep(&mut procs, LSOF);
    procs
}

// Go: TestParseLSOFDeepFDCount.
#[test]
fn fd_count() {
    let procs = parsed();
    assert_eq!((procs[0].open_fds, procs[1].open_fds), (5, 3));
}

// Go: TestParseLSOFDeepFDBreakdown.
#[test]
fn fd_breakdown() {
    let fixture = "COMMAND    PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
App        777   alice    0u   CHR                3,2      0t0     339 /dev/ptmx
App        777   alice    1u   CHR                3,2      0t0     339 /dev/ttys000
App        777   alice    2u   CHR                3,2      0t0     339 /dev/ptyq1
App        777   alice    3u  IPv4 0xabc          0t0     TCP 127.0.0.1:1->127.0.0.1:2 (ESTABLISHED)
App        777   alice    4u  IPv6 0xabc          0t0     TCP [::1]:1->[::1]:2 (ESTABLISHED)
App        777   alice    5u  unix 0xabc          0t0         0 /var/run/app.sock
App        777   alice    6u  IPv4 0xabc          0t0     TCP *:3000 (LISTEN)
App        777   alice    7u  unix 0xabc          0t0         0 /var/run/app2.sock
App        777   alice    8r   REG               1,15        0     100 /tmp/a
App        777   alice    9r   REG               1,15        0     101 /tmp/b
App        777   alice   10r   REG               1,15        0     102 /tmp/c
App        777   alice   11r   REG               1,15        0     103 /tmp/d
App        777   alice   12r   REG               1,15        0     104 /tmp/e
App        777   alice   13r   REG               1,15        0     105 /tmp/f
App        777   alice   14r   REG               1,15        0     106 /tmp/g
App        777   alice   15r   REG               1,15        0     107 /tmp/h
App        777   alice   16r   REG               1,15        0     108 /tmp/i
App        777   alice   17r   REG               1,15        0     109 /tmp/j
App        777   alice   18u  PIPE               0,15      0t0         0
App        777   alice   19u  FIFO               0,15      0t0         0
App        777   alice   20u KQUEUE                                        count=1, state=0
";
    let mut procs = vec![Info { pid: 777, command: "App".into(), ..Info::default() }];
    parse_lsof_deep(&mut procs, fixture);
    let b = procs[0].fd_breakdown.expect("breakdown");
    assert_eq!((procs[0].open_fds, b.total), (21, 21));
    assert_eq!((b.pty, b.socket, b.regular, b.pipe, b.kqueue), (3, 5, 10, 2, 1));
}

// Go: TestParseLSOFDeepFDBreakdownCharDevice.
#[test]
fn fd_breakdown_char_device() {
    let mut procs = vec![Info { pid: 777, ..Info::default() }];
    parse_lsof_deep(&mut procs, "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\nApp 777 alice 0u CHR 3,2 0t0 339 /dev/null\n");
    let b = procs[0].fd_breakdown.expect("breakdown");
    assert_eq!((b.char, b.pty), (1, 0));
}

// Go: TestParseLSOFDeepFDBreakdownAddsUp.
#[test]
fn fd_breakdown_adds_up() {
    for p in parsed() {
        let b = p.fd_breakdown.expect("breakdown");
        let sum = b.pty + b.socket + b.regular + b.dir + b.pipe + b.char + b.kqueue + b.other;
        assert!(sum == p.open_fds && b.total == p.open_fds, "{}: {b:?} vs {}", p.command, p.open_fds);
    }
}

// Go: TestParseLSOFDeepListeningPorts, OutboundConnections, LogFiles, Cwd.
#[test]
fn ports_connections_logs_and_cwd() {
    let procs = parsed();
    assert_eq!(procs[0].listening_ports, [3000]);
    assert!(procs[1].listening_ports.is_empty());
    assert_eq!(procs[0].outbound_connections, ["127.0.0.1:443"]);
    assert!(procs[1].outbound_connections.is_empty());
    assert_eq!(procs[0].log_files, ["/tmp/slack.log"], "1w and 2w on the same log collapse");
    assert!(procs[1].log_files.is_empty());
    assert_eq!((procs[0].cwd.as_str(), procs[1].cwd.as_str()), ("/Applications/Slack.app", "/home/alice"));
}

// Go: TestParseLSOFDeepCwdWithSpaces.
#[test]
fn cwd_with_spaces() {
    let mut procs = vec![Info { pid: 7, ..Info::default() }];
    parse_lsof_deep(&mut procs, "java 7 bob cwd DIR 1,15 512 2 /srv/my app\n");
    assert_eq!(procs[0].cwd, "/srv/my app");
}

// Go: TestIsLogShapedPath.
#[test]
fn log_shaped_paths() {
    for (p, want) in [
        ("/tmp/slack.log", true),
        ("/Users/foo/Library/Logs/Slack/main.log", true),
        ("/Users/foo/Library/Application Support/Slack/Logs/x", true),
        ("/var/log/system.log", true),
        ("/Users/foo/Documents/Catalogs/index", false),
        ("/Users/foo/work/Dialogs/notes", false),
        ("/Applications/Slack.app/Contents/MacOS/Slack", false),
        ("", false),
    ] {
        assert_eq!(is_log_shaped_path(p), want, "{p}");
    }
}

// Go: TestFdIsWritable.
#[test]
fn fd_writable() {
    for (fd, want) in [("1w", true), ("12u", true), ("9r", false), ("", false), ("cwd", false), ("txt", false)] {
        assert_eq!(fd_is_writable(fd), want, "{fd}");
    }
}

// Go: TestParseLSOFDeepUnknownPIDSkipped.
#[test]
fn unknown_pid_is_skipped() {
    let mut procs = vec![Info { pid: 412, ..Info::default() }];
    parse_lsof_deep(&mut procs, LSOF);
    assert!(procs[0].open_fds > 0);
}

// Go: TestEnrichDeepError, TestEnrichDeepParsesPartialOutputOnError, TestCollectAllDeep.
#[test]
fn enrich_handles_empty_and_partial_output() {
    let mut procs = vec![Info { pid: 412, ..Info::default() }];
    enrich(&mut procs, |_| (Vec::new(), None));
    assert_eq!(procs[0].open_fds, 0);
    let mut procs = vec![Info { pid: 412, ..Info::default() }];
    enrich(&mut procs, |_| (Vec::new(), Some("failed".into())));
    assert_eq!(procs[0].open_fds, 0);
    let mut procs = vec![Info { pid: 412, ..Info::default() }];
    enrich(&mut procs, |_| (LSOF.as_bytes().to_vec(), Some("lsof: partial failure".into())));
    assert_eq!((procs[0].open_fds, procs[0].fd_breakdown.map(|b| b.total)), (5, Some(5)));
    let asked = std::cell::RefCell::new(Vec::new());
    let mut procs = two();
    enrich(&mut procs, |cmd| {
        asked.borrow_mut().push(cmd.display());
        (Vec::new(), None)
    });
    assert_eq!(*asked.borrow(), ["lsof -p 412,999"]);
}
