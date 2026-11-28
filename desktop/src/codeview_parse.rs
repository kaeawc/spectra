//! Reading what a code tool printed into the lines the code view shows, and finding the line to
//! mark: the instruction at the frame's offset, the bytecode at its source line, or the line
//! itself in a source file. A tool's output that holds no code is an error, so the next step
//! is tried.

use crate::codeview_plan::{Jvm, Parse, Target, strip_rust_hashes};

/// What a step showed.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Parsed {
    pub lines: Vec<String>,
    /// The line to highlight.
    pub mark: Option<usize>,
    /// What the mark is, or why there isn't one.
    pub note: String,
}

/// Reads `out` from a step of kind `p` for target `t`.
pub fn parse(p: Parse, out: &str, t: &Target) -> Result<Parsed, String> {
    match p {
        Parse::Lldb => lldb(out, t),
        Parse::Objdump => objdump(out, offset(t)),
        Parse::Otool => otool(out, offset(t)),
        Parse::PseudoC => pseudo_c(out),
        Parse::JavaDecompiled => java_decompiled(out, jvm(t)),
        Parse::Javap => javap(out, jvm(t)),
        Parse::Source => Ok(source(out, line(t))),
        Parse::Pretty => pretty(out),
        Parse::PyDecompiled => code_or_err(out, "pycdc").map(|lines| Parsed {
            lines,
            mark: None,
            note: "Decompiled code keeps no line numbers, so the frame's line can't be marked."
                .into(),
        }),
        Parse::PyDis => py_dis(out, line(t)),
    }
}

fn offset(t: &Target) -> Option<u64> {
    match t {
        Target::Native(n) if !n.names.is_empty() => n.offset,
        _ => None,
    }
}

fn line(t: &Target) -> Option<u32> {
    match t {
        Target::Script(s) => s.line,
        Target::Jvm(j) => j.line,
        _ => None,
    }
}

fn jvm(t: &Target) -> Option<&Jvm> {
    match t {
        Target::Jvm(j) => Some(j),
        _ => None,
    }
}

/// Drops terminal colour codes.
fn plain(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut it = s.chars().peekable();
    while let Some(c) = it.next() {
        if c == '\u{1b}' {
            if it.peek() == Some(&'[') {
                it.next();
                for d in it.by_ref() {
                    if d.is_ascii_alphabetic() {
                        break;
                    }
                }
            }
            continue;
        }
        out.push(c);
    }
    out
}

fn code_or_err(out: &str, tool: &str) -> Result<Vec<String>, String> {
    let lines: Vec<String> = out
        .lines()
        .map(|l| plain(l).trim_end().to_owned())
        .collect();
    let start = lines.iter().position(|l| !l.trim().is_empty());
    let end = lines.iter().rposition(|l| !l.trim().is_empty());
    match (start, end) {
        (Some(s), Some(e)) => Ok(lines[s..=e].to_vec()),
        _ => Err(format!("{tool} printed nothing")),
    }
}

/// `libdispatch.dylib[0x18035d4ac] <+12>: blraaz x1` → the address in brackets.
fn bracket_addr(l: &str) -> Option<u64> {
    let open = l.find("[0x")?;
    let close = l[open..].find(']')? + open;
    u64::from_str_radix(&l[open + 3..close], 16).ok()
}

/// `<+12>:` → 12.
fn rel(l: &str) -> Option<u64> {
    let i = l.find("<+")?;
    let j = l[i..].find('>')? + i;
    l[i + 2..j].parse().ok()
}

fn lldb(out: &str, t: &Target) -> Result<Parsed, String> {
    let (image, name) = match t {
        Target::Native(n) => (
            n.image.as_str(),
            n.names.first().map(String::as_str).unwrap_or(""),
        ),
        _ => ("", ""),
    };
    let mut at = None;
    let mut lines = Vec::new();
    for l in out.lines() {
        let l = l.trim_end();
        if let Some(a) = l.strip_prefix("CODEVIEW_AT=0x") {
            at = u64::from_str_radix(a.trim(), 16).ok();
            continue;
        }
        if let Some(e) = l.strip_prefix("CODEVIEW_ERR=") {
            return Err(format!("lldb couldn't load the binary: {e}"));
        }
        if l == "CODEVIEW_NONE" {
            let place = if image.is_empty() {
                "the process or what it links".to_owned()
            } else {
                image.to_owned()
            };
            return Err(if name.is_empty() {
                format!("lldb found no code at that offset in {place}")
            } else {
                format!("lldb found no function named {name} in {place}")
            });
        }
        if l.starts_with("(lldb) ") || l.starts_with("Current executable set to") {
            continue;
        }
        if l.starts_with("Traceback (most recent call last)") {
            let last = out
                .lines()
                .rev()
                .find(|x| !x.trim().is_empty())
                .unwrap_or("");
            return Err(format!("lldb's script failed: {}", last.trim()));
        }
        lines.push(l.to_owned());
    }
    let lines = code_or_err(&lines.join("\n"), "lldb")?;
    let mark = at
        .and_then(|a| lines.iter().position(|l| bracket_addr(l) == Some(a)))
        .or_else(|| {
            let o = offset(t)?;
            lines.iter().position(|l| rel(l) == Some(o))
        });
    let functions = lines
        .iter()
        .filter(|l| l.ends_with(':') && l.contains('`'))
        .count();
    // `Module[0xADDR] <+N>: insn` reads as `0xADDR <+N>: insn` under its `Module`symbol:` header.
    let lines: Vec<String> = lines
        .into_iter()
        .map(|l| match l.find("[0x") {
            Some(i) if !l.contains('`') || l.find('`') > Some(i) => {
                strip_rust_hashes(&l[i + 1..].replacen(']', "", 1))
            }
            _ => strip_rust_hashes(&l),
        })
        .collect();
    let note = mark_note(t, mark.is_some(), functions);
    Ok(Parsed { lines, mark, note })
}

fn mark_note(t: &Target, marked: bool, functions: usize) -> String {
    let Target::Native(n) = t else {
        return String::new();
    };
    match (n.offset, marked, n.names.is_empty()) {
        (None, _, _) => "The frame gives no offset, so no instruction is marked.".into(),
        (Some(o), true, false) if functions > 1 => {
            format!("+{o} is marked in the first of {functions} functions with this name.")
        }
        (Some(o), true, false) => format!("The instruction at +{o} is marked."),
        (Some(o), true, true) => format!("The instruction at 0x{o:x} into the image is marked."),
        (Some(o), false, _) => format!("Couldn't find +{o} in the listing to mark it."),
    }
}

/// A leading hex address, `100003e30:` or `0000000100003e30\t`.
fn lead_addr(l: &str) -> Option<u64> {
    let t = l.trim_start();
    let end = t.find(|c: char| !c.is_ascii_hexdigit()).unwrap_or(t.len());
    if end < 4 {
        return None;
    }
    u64::from_str_radix(&t[..end], 16).ok()
}

fn objdump(out: &str, off: Option<u64>) -> Result<Parsed, String> {
    let all: Vec<&str> = out.lines().collect();
    // `0000000100003e30 <_main>:` starts a function.
    let Some(start) = all
        .iter()
        .position(|l| l.trim_end().ends_with(">:") && l.contains(" <"))
    else {
        return Err("llvm-objdump found no symbol by that name".into());
    };
    let base = lead_addr(all[start]);
    let lines: Vec<String> = all[start..]
        .iter()
        .map(|l| strip_rust_hashes(l.trim_end()))
        .collect();
    let lines = code_or_err(&lines.join("\n"), "llvm-objdump")?;
    let mark = match (base, off) {
        (Some(b), Some(o)) => lines
            .iter()
            .skip(1)
            .position(|l| l.contains(':') && lead_addr(l) == Some(b + o))
            .map(|i| i + 1),
        _ => None,
    };
    let note = native_note(off, mark.is_some());
    Ok(Parsed { lines, mark, note })
}

fn native_note(off: Option<u64>, marked: bool) -> String {
    match (off, marked) {
        (None, _) => "The frame gives no offset, so no instruction is marked.".into(),
        (Some(o), true) => format!("The instruction at +{o} is marked."),
        (Some(o), false) => format!("Couldn't find +{o} in the listing to mark it."),
    }
}

fn otool(out: &str, off: Option<u64>) -> Result<Parsed, String> {
    let all: Vec<&str> = out.lines().collect();
    // After the file and section lines, `_symbol:` labels the function; the next label ends it.
    let label =
        |l: &str| l.ends_with(':') && !l.starts_with(char::is_whitespace) && lead_addr(l).is_none();
    let Some(start) = all.iter().skip(1).position(|l| label(l)).map(|i| i + 1) else {
        return Err("otool found no symbol by that name".into());
    };
    let end = all[start + 1..]
        .iter()
        .position(|l| label(l))
        .map_or(all.len(), |i| start + 1 + i);
    let lines: Vec<String> = all[start..end]
        .iter()
        .map(|l| strip_rust_hashes(l.trim_end()))
        .collect();
    let lines = code_or_err(&lines.join("\n"), "otool")?;
    let base = lines.get(1).and_then(|l| lead_addr(l));
    let mark = match (base, off) {
        (Some(b), Some(o)) => lines.iter().position(|l| lead_addr(l) == Some(b + o)),
        _ => None,
    };
    let note = native_note(off, mark.is_some());
    Ok(Parsed { lines, mark, note })
}

fn pseudo_c(out: &str) -> Result<Parsed, String> {
    let lines = code_or_err(out, "the decompiler")?;
    let text = lines.join("\n");
    let complaint = [
        "Cannot find",
        "Invalid command",
        "Unknown command",
        "You need to install",
        "r_core_cmd",
        "Cannot open",
    ];
    if !text.contains('{') || complaint.iter().any(|c| text.contains(c)) {
        let first = lines.first().map(String::as_str).unwrap_or("");
        return Err(format!("the decompiler gave no code: {}", first.trim()));
    }
    Ok(Parsed {
        lines,
        mark: None,
        note: "Decompiled code doesn't map back to the crash offset; the disassembly does.".into(),
    })
}

/// A method's simple class name, `Ledger` for `com.acme.Ledger$Entry` is `Entry`.
fn simple(class: &str) -> &str {
    let tail = class.rsplit('.').next().unwrap_or(class);
    tail.rsplit('$').next().unwrap_or(tail)
}

/// The name a declaration of `j`'s method has: the class's for a constructor.
fn decl_name(j: &Jvm) -> &str {
    if j.method == "<init>" {
        simple(&j.class)
    } else {
        &j.method
    }
}

fn java_decompiled(out: &str, j: Option<&Jvm>) -> Result<Parsed, String> {
    let lines = code_or_err(out, "the decompiler")?;
    if !lines.iter().any(|l| {
        l.contains("class ")
            || l.contains("interface ")
            || l.contains("enum ")
            || l.contains("record ")
    }) {
        let first = lines.first().map(String::as_str).unwrap_or("");
        return Err(format!("the decompiler gave no class: {}", first.trim()));
    }
    let mark = j.and_then(|j| {
        let needle = format!(" {}(", decl_name(j));
        lines.iter().position(|l| {
            let t = l.trim();
            l.contains(&needle)
                && !t.ends_with(';')
                && !t.starts_with("return")
                && !t.contains(" = ")
        })
    });
    let note = match (j, mark) {
        (Some(j), Some(_)) => match j.line {
            Some(n) => format!(
                "{} is marked. Decompiled code has no line numbers, so line {n} can't be placed; javap's bytecode can.",
                j.method
            ),
            None => format!("{} is marked.", j.method),
        },
        (Some(j), None) => format!(
            "Couldn't find {} in the decompiled class to mark it.",
            j.method
        ),
        (None, _) => String::new(),
    };
    Ok(Parsed { lines, mark, note })
}

fn javap(out: &str, j: Option<&Jvm>) -> Result<Parsed, String> {
    let all: Vec<&str> = out.lines().map(str::trim_end).collect();
    if all.iter().any(|l| l.starts_with("Error: class not found")) {
        let class = j.map_or("the class", |j| j.class.as_str());
        return Err(format!(
            "javap couldn't find {class} in the JVM's classpath or the JDK"
        ));
    }
    let Some(head) = all.iter().position(|l| {
        (l.contains("class ") || l.contains("interface ")) && l.trim_end().ends_with('{')
    }) else {
        let first = all
            .iter()
            .find(|l| !l.trim().is_empty())
            .copied()
            .unwrap_or("");
        return Err(format!("javap gave no class: {}", first.trim()));
    };
    let Some(j) = j else {
        return Ok(Parsed {
            lines: all.iter().map(|l| (*l).to_owned()).collect(),
            ..Parsed::default()
        });
    };
    // Members start at two spaces of indent; their bodies sit deeper.
    let member = |l: &str| l.starts_with("  ") && !l.starts_with("   ") && !l.trim().is_empty();
    let needle = format!(" {}(", decl_name(j));
    let alt = format!(".{}(", decl_name(j));
    let mut keep: Vec<String> = all[0..=head].iter().map(|l| (*l).to_owned()).collect();
    let mut i = head + 1;
    let mut found = 0;
    let mut mark = None;
    while i < all.len() {
        let l = all[i];
        if member(l) && (l.contains(&needle) || l.contains(&alt)) {
            let end = all[i + 1..]
                .iter()
                .position(|x| member(x) || x.starts_with('}'))
                .map_or(all.len(), |n| i + 1 + n);
            let block = &all[i..end];
            keep.push(String::new());
            if mark.is_none()
                && let Some(n) = j.line
                && let Some(code_off) = block.iter().find_map(|x| {
                    let rest = x.trim().strip_prefix("line ")?;
                    let (ln, off) = rest.split_once(':')?;
                    (ln.trim().parse::<u32>().ok()? == n).then(|| off.trim().to_owned())
                })
            {
                mark = block
                    .iter()
                    .position(|x| x.trim_start().starts_with(&format!("{code_off}: ")))
                    .map(|p| keep.len() + p);
            }
            keep.extend(block.iter().map(|x| (*x).to_owned()));
            found += 1;
            i = end;
            continue;
        }
        i += 1;
    }
    if found == 0 {
        return Ok(Parsed {
            lines: all.iter().map(|l| (*l).to_owned()).collect(),
            mark: None,
            note: format!(
                "Couldn't find {} in the class, so all of it is shown.",
                j.method
            ),
        });
    }
    keep.push("}".into());
    let lines = keep;
    let note = match (j.line, mark) {
        (Some(n), Some(m)) => format!(
            "Line {n} starts at the bytecode marked ({}).",
            lines[m].trim().split(':').next().unwrap_or("")
        ),
        (Some(n), None) => {
            format!("javap's line table doesn't list line {n}, so nothing is marked.")
        }
        (None, _) => "The frame gives no line, so nothing is marked.".into(),
    };
    Ok(Parsed { lines, mark, note })
}

/// Each line numbered, the frame's line marked.
fn source(out: &str, line: Option<u32>) -> Parsed {
    let lines: Vec<String> = out
        .lines()
        .enumerate()
        .map(|(i, l)| format!("{:>5}  {}", i + 1, l.trim_end()))
        .collect();
    let mark = line
        .map(|n| n as usize)
        .filter(|n| *n >= 1 && *n <= lines.len())
        .map(|n| n - 1);
    let note = match (line, mark) {
        (Some(n), Some(_)) => format!("Line {n} is marked."),
        (Some(n), None) => format!("The file has fewer than {n} lines; it may have changed since."),
        (None, _) => "The frame gives no line, so nothing is marked.".into(),
    };
    Parsed { lines, mark, note }
}

fn pretty(out: &str) -> Result<Parsed, String> {
    let lines = code_or_err(out, "the formatter")?;
    let numbered = lines
        .iter()
        .enumerate()
        .map(|(i, l)| format!("{:>5}  {l}", i + 1))
        .collect();
    Ok(Parsed {
        lines: numbered,
        mark: None,
        note: "The file is minified and was pretty-printed, so the frame's line and column can't be marked.".into(),
    })
}

/// Whether a JavaScript file is minified: a very long line, or a few long ones carrying it all.
pub fn minified(text: &str) -> bool {
    let n = text.lines().count().max(1);
    text.lines().any(|l| l.len() > 1000) || (text.len() > 2000 && text.len() / n > 300)
}

fn py_dis(out: &str, line: Option<u32>) -> Result<Parsed, String> {
    let lines = code_or_err(out, "python3 -m dis")?;
    let mark = line.and_then(|n| {
        let want = n.to_string();
        lines.iter().position(|l| {
            l.starts_with(|c: char| c == ' ' || c.is_ascii_digit())
                && l.split_whitespace().next() == Some(want.as_str())
        })
    });
    let note = match (line, mark) {
        (Some(n), Some(_)) => format!("Line {n}'s bytecode starts at the line marked."),
        (Some(n), None) => format!("The bytecode doesn't list line {n}, so nothing is marked."),
        (None, _) => String::new(),
    };
    Ok(Parsed { lines, mark, note })
}

/// Swift (`$s…`) and C++ (`__Z…`) symbols in disassembly, for a demangler.
pub fn mangled(lines: &[String], swift: bool) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for l in lines {
        for w in l.split(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '$')) {
            let hit = if swift {
                w.starts_with("$s") || w.starts_with("_$s")
            } else {
                w.starts_with("__Z")
            };
            if hit && w.len() > 4 && !out.iter().any(|o| o == w) {
                out.push(w.to_owned());
            }
            if out.len() >= 200 {
                return out;
            }
        }
    }
    out
}

/// Puts each demangled name in place of its mangled one. `demangled` is the demangler's output,
/// one line per name; a count that doesn't match changes nothing.
pub fn demangle(lines: &mut [String], names: &[String], demangled: &str) {
    let got: Vec<&str> = demangled.lines().collect();
    if got.len() != names.len() {
        return;
    }
    for l in lines.iter_mut() {
        for (m, d) in names.iter().zip(&got) {
            if l.contains(m.as_str()) && !d.trim().is_empty() {
                *l = l.replace(m.as_str(), d.trim());
            }
        }
    }
}

#[cfg(test)]
#[path = "codeview_parse_tests.rs"]
mod tests;
