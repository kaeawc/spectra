//! Go's `review_test.go`, plus checks of the binary readers against real executables.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

use spectra_base::fs::OsFs;
use spectra_base::hostos::Kind;
use spectra_base::proc::{CancelToken, FakeRunner};

use super::basics::ADHOC_CODESIGN;
use super::fixtures::*;
use crate::classify::reader::{FileReader, ReadAt, Section};
use crate::classify::runtime::{bound_symtab, inspect_binary, macho_verdict};
use crate::classify::{Classifier, DEFAULT_MAX_SCAN_BYTES, Options, Runtime, SigningStatus, macho};

// Go: TestTransientReadFailureIsNotCachedAndRetries.
#[test]
fn transient_read_failure_is_not_cached_and_retries() {
    let fs = FakeFs::new();
    fs.add("/proc/9/exe", build_elf(&[(".rodata", &rust_data())]));
    fs.deny("/proc/9/exe", true);
    let store = MemStore::new();
    let c = Classifier::new(with_store(opts(Kind::Linux, fs.clone(), Arc::new(FakeRunner::new())), store.clone()));
    let one = input(9, 501, "tool", "/opt/tool", "/opt/tool");
    assert_eq!(classify_one(&c, one.clone()).runtime, Runtime::Unknown, "unknown while unreadable");
    assert_eq!(store.puts(), 0, "unreadable verdict persisted");
    fs.deny("/proc/9/exe", false);
    assert_eq!(classify_one(&c, one).runtime, Runtime::Rust, "rust after the read recovers");
    assert_eq!(store.puts(), 1, "completed inspection not persisted");
}

// Go: TestSigningFailureIsNotCachedAndRetries.
#[test]
fn signing_failure_is_not_cached_and_retries() {
    let fs = FakeFs::new();
    let exe = "/opt/homebrew/bin/rg";
    fs.add(exe, build_macho(CPU_ARM, &[Sect("__TEXT", "__cstring", b"c\0")]));
    let runner = Arc::new(FlakyRunner { inner: codesign_fake(ADHOC_CODESIGN), fail: AtomicBool::new(true) });
    let store = MemStore::new();
    let c = Classifier::new(with_store(opts(Kind::Darwin, fs, runner.clone()), store.clone()));
    let one = input(3, 501, "rg", exe, exe);
    assert_eq!(classify_one(&c, one.clone()).signing.map(|s| s.status), Some(SigningStatus::Unknown));
    assert_eq!(store.puts(), 0, "failed signing persisted to disk");
    runner.fail.store(false, Ordering::SeqCst);
    assert_eq!(classify_one(&c, one).signing.map(|s| s.status), Some(SigningStatus::Adhoc));
}

// Go: TestPoolDispatchesNothingAfterCancel.
#[test]
fn pool_dispatches_nothing_after_cancel() {
    let cancel = CancelToken::new();
    cancel.cancel();
    let calls = AtomicUsize::new(0);
    let c = Classifier::new(Options { workers: 4, ..opts(Kind::Darwin, FakeFs::new(), Arc::new(FakeRunner::new())) });
    let err = c.engine.pool(&cancel, 50, &|_| {
        calls.fetch_add(1, Ordering::SeqCst);
    });
    assert!(err.is_some() && calls.load(Ordering::SeqCst) == 0);
    // Not cancelled: every job runs once, across the workers.
    let fresh = CancelToken::new();
    assert!(
        c.engine
            .pool(&fresh, 50, &|_| {
                calls.fetch_add(1, Ordering::SeqCst);
            })
            .is_none()
    );
    assert_eq!(calls.load(Ordering::SeqCst), 50);
}

// Go: TestInspectBinarySkipsOversizedSymbolTables.
#[test]
fn inspect_binary_skips_oversized_symbol_tables() {
    let mut elf = build_elf(&[(".rodata", &rust_data()), (".symtab", b"x")]);
    let shoff = u64::from_le_bytes(elf[40..48].try_into().expect("shoff")) as usize;
    let hdr = shoff + 64 * 2; // section 2 is .symtab
    elf[hdr + 4..hdr + 8].copy_from_slice(&2u32.to_le_bytes()); // SHT_SYMTAB
    elf[hdr + 32..hdr + 40].copy_from_slice(&(1u64 << 40).to_le_bytes());
    let fs = FakeFs::new();
    fs.add("/bin/huge", elf);
    let (v, err) = inspect_binary(fs.as_ref(), "/bin/huge", DEFAULT_MAX_SCAN_BYTES, "arm64");
    assert!(err.is_none() && v.runtime == Runtime::Rust && v.evidence.contains("symbol table too large"), "{v:?}");
    fs.add("/bin/plain", build_elf(&[(".rodata", b"hello")]));
    let (v, _) = inspect_binary(fs.as_ref(), "/bin/plain", DEFAULT_MAX_SCAN_BYTES, "arm64");
    assert!(!v.evidence.contains("symbol table"), "unmarked binary consulted symbols: {v:?}");
}

// Go: TestBoundSymtabZeroesOversizedTable.
#[test]
fn bound_symtab_zeroes_oversized_table() {
    let huge = symtab_only_macho(1 << 28, 1 << 28);
    assert!(macho::parse(&huge).is_err(), "the unbounded table must fail to load");
    let (mr, skipped) = bound_symtab(&huge, DEFAULT_MAX_SCAN_BYTES).expect("Mach-O");
    assert!(skipped, "oversized symtab not flagged");
    let sr = Section { r: &mr, off: 0, size: huge.len() as u64 };
    let f = macho::parse(&sr).expect("parse over the bounded reader");
    assert!(f.symtab.as_ref().is_some_and(Vec::is_empty));
    let small = symtab_only_macho(0, 0);
    assert!(!bound_symtab(&small, DEFAULT_MAX_SCAN_BYTES).expect("Mach-O").1, "empty symtab flagged");
    let script = b"#!/bin/sh\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n".to_vec();
    assert!(bound_symtab(&script, 1).is_none(), "non-Mach-O accepted");
}

// Go: TestMachoVerdictMarksSkippedSymbols.
#[test]
fn macho_verdict_marks_skipped_symbols() {
    let bin = build_macho(CPU_ARM, &[Sect("__TEXT", "__const", &rust_data())]);
    let f = macho::parse(&bin).expect("parse");
    let v = macho_verdict(&f, &bin, DEFAULT_MAX_SCAN_BYTES, true);
    assert!(v.runtime == Runtime::Rust && v.evidence.contains("symbol table too large"), "{v:?}");
}

fn own_executable() -> Option<String> {
    std::env::current_exe().ok().map(|p| p.to_string_lossy().into_owned())
}

// Go: TestBoundSymtabKeepsDysymtabConsistent. A skipped symbol table must still parse: a
// dysymtab that indexes past an empty symtab is rejected (seen live on iTerm2).
#[test]
fn bound_symtab_keeps_dysymtab_consistent() {
    let Some(exe) = own_executable() else { return };
    let f = FileReader { fs: &OsFs, path: exe };
    let Some((mr, skipped)) = bound_symtab(&f, 0) else { return }; // not a thin Mach-O
    assert!(skipped, "limit 0 did not skip the symbol table");
    let mf = macho::parse(&mr).expect("Mach-O with a skipped symbol table parses");
    assert!(mf.symtab.as_ref().is_none_or(Vec::is_empty), "symbols were loaded");
}

/// The test binary is a Rust program with its symbols: the readers must parse a real Mach-O or
/// ELF end to end and find `std::rt::lang_start`.
#[test]
fn the_test_binary_is_rust() {
    let Some(exe) = own_executable() else { return };
    let (v, err) = inspect_binary(&OsFs, &exe, DEFAULT_MAX_SCAN_BYTES, "");
    assert!(err.is_none(), "{err:?}");
    assert_eq!(v.runtime, Runtime::Rust, "{v:?}");
    let f = FileReader { fs: &OsFs, path: exe };
    let mut magic = [0u8; 4];
    assert_eq!(f.read_at(0, &mut magic).expect("read"), 4);
}
