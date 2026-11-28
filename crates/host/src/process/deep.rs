//! `--deep`: open descriptors, listening ports, outbound connections, log files and the working
//! directory from one batched `lsof -p pid1,pid2,...` (Go's `deep.go`).

use std::collections::HashMap;

use spectra_base::proc::Cmd;

use super::{FdBreakdown, Info};

/// Go's `enrichDeep`. A failing `lsof` that still printed rows is parsed.
pub(super) fn enrich(procs: &mut [Info], run: impl Fn(&Cmd) -> (Vec<u8>, Option<String>)) {
    let pids: Vec<String> = procs.iter().map(|p| p.pid.to_string()).collect();
    let (out, err) = run(&Cmd::new("lsof", ["-p".to_string(), pids.join(",")]));
    if err.is_some() && out.is_empty() {
        return;
    }
    parse_lsof_deep(procs, &String::from_utf8_lossy(&out));
}

#[derive(Default)]
struct DeepResult {
    fd_count: i64,
    breakdown: Option<FdBreakdown>,
    listen_ports: Vec<i64>,
    outbound: Vec<String>,
    log_files: Vec<String>,
    cwd: String,
}

/// Go's `parseLSOFDeep`: merges `lsof` rows (COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE
/// NAME) into `procs`.
pub fn parse_lsof_deep(procs: &mut [Info], out: &str) {
    let mut idx: HashMap<i64, usize> = HashMap::new();
    for (i, p) in procs.iter().enumerate() {
        idx.insert(p.pid, i);
    }
    let mut results: HashMap<i64, DeepResult> = HashMap::new();
    for line in out.split('\n') {
        record_line(&idx, &mut results, line);
    }
    for (pid, mut r) in results {
        let p = &mut procs[idx[&pid]];
        p.open_fds = r.fd_count;
        p.fd_breakdown = r.breakdown;
        p.cwd = r.cwd;
        if !r.listen_ports.is_empty() {
            r.listen_ports.sort_unstable();
            p.listening_ports = r.listen_ports;
        }
        if !r.outbound.is_empty() {
            r.outbound.sort();
            p.outbound_connections = r.outbound;
        }
        if !r.log_files.is_empty() {
            r.log_files.sort();
            r.log_files.dedup();
            p.log_files = r.log_files;
        }
    }
}

fn record_line(idx: &HashMap<i64, usize>, results: &mut HashMap<i64, DeepResult>, line: &str) {
    let fields: Vec<&str> = line.split_whitespace().collect();
    if fields.len() < 5 {
        return;
    }
    let Ok(pid) = crate::goerr::parse_int(fields[1]) else { return };
    if !idx.contains_key(&pid) {
        return;
    }
    let r = results.entry(pid).or_default();
    let fd = fields[3];
    let name = if fields.len() >= 9 { fields[8..].join(" ") } else { String::new() };
    if fd == "cwd" && fields.len() >= 9 {
        r.cwd = name.clone();
    }
    record_fd(r, fd, fields[4], &name);
    if fields.len() >= 9 && fields[7].eq_ignore_ascii_case("TCP") {
        record_tcp_name(r, &name);
    }
    if fields.len() >= 9 && fields[4].eq_ignore_ascii_case("REG") && fd_is_writable(fd) {
        record_log_file(r, &name);
    }
}

/// Rows whose FD starts with a digit are real open descriptors.
fn record_fd(r: &mut DeepResult, fd: &str, typ: &str, name: &str) {
    if !fd.as_bytes().first().is_some_and(u8::is_ascii_digit) {
        return;
    }
    r.fd_count += 1;
    let b = r.breakdown.get_or_insert_with(FdBreakdown::default);
    b.total += 1;
    match typ {
        "IPv4" | "IPv6" | "unix" => b.socket += 1,
        "REG" => b.regular += 1,
        "DIR" => b.dir += 1,
        "PIPE" | "FIFO" => b.pipe += 1,
        "KQUEUE" => b.kqueue += 1,
        "CHR" if is_pty_path(name) => b.pty += 1,
        "CHR" => b.char += 1,
        _ => b.other += 1,
    }
}

fn is_pty_path(name: &str) -> bool {
    name.starts_with("/dev/ttys") || name.starts_with("/dev/pty") || name == "/dev/ptmx"
}

/// Go's `fdIsWritable`: `12u` and `13w` are writable, `9r` is not.
pub(crate) fn fd_is_writable(fd: &str) -> bool {
    matches!(fd.as_bytes().last(), Some(b'w' | b'u'))
}

/// Go's `isLogShapedPath`: a `.log` file, or one under a `/Logs/` path segment.
pub fn is_log_shaped_path(path: &str) -> bool {
    !path.is_empty() && (path.ends_with(".log") || path.contains("/Logs/"))
}

fn record_log_file(r: &mut DeepResult, name: &str) {
    // lsof can append flags like "(deleted)" after the path.
    let path = name.split(' ').next().unwrap_or(name);
    if is_log_shaped_path(path) {
        r.log_files.push(path.to_string());
    }
}

fn record_tcp_name(r: &mut DeepResult, name: &str) {
    if name.contains("(LISTEN)") {
        let port = parse_listen_port(name);
        if port > 0 {
            r.listen_ports.push(port);
        }
        return;
    }
    let remote = parse_remote_address(name);
    if !remote.is_empty() {
        r.outbound.push(remote);
    }
}

fn parse_listen_port(name: &str) -> i64 {
    let addr = name.strip_suffix(" (LISTEN)").unwrap_or(name);
    let Some(colon) = addr.rfind(':') else { return 0 };
    match crate::goerr::parse_int(&addr[colon + 1..]) {
        Ok(p) if p > 0 => p,
        _ => 0,
    }
}

fn parse_remote_address(name: &str) -> String {
    let Some(idx) = name.find("->") else { return String::new() };
    let remote = &name[idx + 2..];
    remote.split(' ').next().unwrap_or(remote).to_string()
}

#[cfg(test)]
mod tests;
