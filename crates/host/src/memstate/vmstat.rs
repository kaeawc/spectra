//! Fixture parsers for macOS readings (Go's `internal/memstate/parse.go`): `vm_stat(1)` text and
//! the `xsw_usage` sysctl struct.

use super::{MemoryState, PressureLevel, SwapUsage, finish};
use crate::goerr::parse_uint;

/// Go's `ParseVMStat`: `vm_stat(1)` output, for the fields that overlap `host_statistics64`.
/// Used by fixture tests and by people comparing Spectra samples with `vm_stat`. (Go fails on
/// a line over 64 KiB, which `vm_stat` never prints; this reads any line.)
pub fn parse_vm_stat(text: &str, page_size_bytes: u64, physical_bytes: u64) -> MemoryState {
    let mut ms = MemoryState { page_size_bytes, physical_bytes, pressure_level: PressureLevel::Unknown, ..MemoryState::default() };
    for line in text.lines() {
        if let Some((key, pages)) = parse_vm_stat_line(line) {
            apply_vm_stat_pages(&mut ms, &key, pages);
        }
    }
    finish(&mut ms);
    ms
}

/// Go's `ParseXSWUsage`: the stable leading fields of Darwin's `xsw_usage` struct from a
/// little-endian fixture.
pub fn parse_xsw_usage(data: &[u8]) -> Result<SwapUsage, String> {
    let (Some(total), Some(free), Some(used), Some(encrypted)) =
        (le_u64(data, 0), le_u64(data, 8), le_u64(data, 16), data.get(24..28).and_then(|b| <[u8; 4]>::try_from(b).ok()))
    else {
        return Err(format!("xsw_usage fixture too short: {} bytes", data.len()));
    };
    Ok(SwapUsage { total_bytes: total, free_bytes: free, used_bytes: used, encrypted: u32::from_le_bytes(encrypted) != 0 })
}

fn le_u64(data: &[u8], at: usize) -> Option<u64> {
    data.get(at..at + 8).and_then(|b| <[u8; 8]>::try_from(b).ok()).map(u64::from_le_bytes)
}

/// RE2's `\s`.
fn is_re2_space(c: char) -> bool {
    matches!(c, ' ' | '\t' | '\n' | '\x0c' | '\r')
}

/// Go's `^(.+):\s+([0-9.]+)\.?$` on the trimmed line: the lowercased key and the page count
/// with its dots (thousands separators and the final period) removed.
fn parse_vm_stat_line(line: &str) -> Option<(String, u64)> {
    let line = line.trim();
    let value_start = line.trim_end_matches(|c: char| c.is_ascii_digit() || c == '.').len();
    let value = &line[value_start..];
    let before = &line[..value_start];
    let spaces = before.trim_end_matches(is_re2_space);
    if value.is_empty() || spaces.len() == before.len() {
        return None;
    }
    let key = spaces.strip_suffix(':').filter(|k| !k.is_empty())?;
    let pages = parse_uint(&value.replace('.', "")).ok()?;
    Some((key.to_lowercase(), pages))
}

fn apply_vm_stat_pages(ms: &mut MemoryState, key: &str, pages: u64) {
    let bytes = pages.wrapping_mul(ms.page_size_bytes);
    match key {
        "pages free" => ms.free = bytes,
        "pages active" => ms.active = bytes,
        "pages inactive" => ms.inactive = bytes,
        "pages speculative" => ms.speculative = bytes,
        "pages wired down" => ms.wired = bytes,
        "pages purgeable" => ms.purgeable = bytes,
        "file-backed pages" => ms.file_backed = bytes,
        "anonymous pages" => ms.anonymous = bytes,
        "pages occupied by compressor" => ms.compressor_occupied = bytes,
        "pages stored in compressor" => ms.compressor_stored = bytes,
        "compressions" => ms.compressions = pages,
        "decompressions" => ms.decompressions = pages,
        "swapins" => ms.swap_ins = pages,
        "swapouts" => ms.swap_outs = pages,
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn vm_stat_lines_follow_the_go_regexp() {
        assert_eq!(parse_vm_stat_line("Pages free:     10."), Some(("pages free".into(), 10)));
        assert_eq!(parse_vm_stat_line("  Compressions:   1.234.  "), Some(("compressions".into(), 1234)));
        assert_eq!(parse_vm_stat_line("Mach Virtual Memory Statistics: (page size of 4096 bytes)"), None);
        assert_eq!(parse_vm_stat_line("Pages free:10."), None, "needs whitespace after the colon");
        assert_eq!(parse_vm_stat_line(": 10."), None, "needs a key");
        assert_eq!(parse_vm_stat_line("a: b: 7"), Some(("a: b".into(), 7)), "the key is greedy");
        assert_eq!(parse_vm_stat_line("Pages free:   ..."), None, "dots alone are no number");
        assert_eq!(parse_vm_stat_line("Pages free: 12a"), None);
    }
}
