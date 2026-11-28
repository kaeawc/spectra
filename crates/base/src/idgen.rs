//! Opaque string ids (Go's `internal/idgen`). Production code uses [`UuidGen`]; tests substitute
//! [`Sequence`] to make ids deterministic.
//!
//! Content-addressed values such as cache keys stay deterministic without this module. It is for
//! records and request-shaped values that must be generated fresh while tests still need exact
//! assertions.

use std::fmt::Write as _;
use std::io::Read;
use std::sync::atomic::{AtomicU64, Ordering};

/// Produces successive ids. Implementations are safe for concurrent use and never return the
/// empty string.
pub trait IdGen: Send + Sync {
    fn next(&self) -> String;
}

/// Where random bytes come from, so tests need not touch the OS.
pub trait RandomSource: Send + Sync {
    fn fill(&self, buf: &mut [u8]);
}

/// The operating system's CSPRNG, read from `/dev/urandom` (macOS and Linux).
#[derive(Clone, Copy, Debug, Default)]
pub struct OsRandom;

impl RandomSource for OsRandom {
    /// Panics if the device cannot be read, as Go's `crypto/rand.Read` failure does in `idgen`.
    fn fill(&self, buf: &mut [u8]) {
        let read = std::fs::File::open("/dev/urandom").and_then(|mut f| f.read_exact(buf));
        if let Err(e) = read {
            panic!("idgen: reading /dev/urandom failed: {e}");
        }
    }
}

/// Random 16-byte ids: each call yields 32 lowercase hex characters with no dashes.
#[derive(Debug, Default)]
pub struct UuidGen<R = OsRandom> {
    random: R,
}

impl<R: RandomSource> UuidGen<R> {
    pub fn with_random(random: R) -> Self {
        UuidGen { random }
    }
}

impl<R: RandomSource> IdGen for UuidGen<R> {
    fn next(&self) -> String {
        let mut buf = [0u8; 16];
        self.random.fill(&mut buf);
        let mut out = String::with_capacity(32);
        for b in buf {
            let _ = write!(out, "{b:02x}");
        }
        out
    }
}

/// The generator for callers that cannot reach a composition root (Go's `idgen.Default`).
/// Prefer injecting an [`IdGen`] explicitly.
pub fn default_generator() -> UuidGen {
    UuidGen::default()
}

/// Prefixed counter ids: `id-1`, `id-2`, ... A custom prefix may be supplied.
#[derive(Debug)]
pub struct Sequence {
    prefix: String,
    n: AtomicU64,
}

impl Sequence {
    /// An empty prefix is replaced with `id`.
    pub fn new(prefix: &str) -> Self {
        let prefix = if prefix.is_empty() { "id" } else { prefix };
        Sequence { prefix: prefix.to_string(), n: AtomicU64::new(0) }
    }

    /// Returns the counter to zero; the next id is numbered 1 again.
    pub fn reset(&self) {
        self.n.store(0, Ordering::SeqCst);
    }
}

impl IdGen for Sequence {
    fn next(&self) -> String {
        let n = self.n.fetch_add(1, Ordering::SeqCst).wrapping_add(1);
        format!("{}-{n}", self.prefix)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;
    use std::sync::Arc;

    fn is_hex32(s: &str) -> bool {
        s.len() == 32 && s.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    }

    #[test]
    fn uuid_next_is_hex_and_unique() {
        let g = default_generator();
        let mut seen = HashSet::new();
        for i in 0..1000 {
            let id = g.next();
            assert!(is_hex32(&id), "id {id:?} does not match 32-char lowercase hex");
            assert!(seen.insert(id.clone()), "duplicate id {id:?} at iteration {i}");
        }
    }

    #[test]
    fn uuid_formats_the_bytes_it_is_given() {
        struct Fixed;
        impl RandomSource for Fixed {
            fn fill(&self, buf: &mut [u8]) {
                for (i, b) in buf.iter_mut().enumerate() {
                    *b = (i as u8) * 17;
                }
            }
        }
        assert_eq!(UuidGen::with_random(Fixed).next(), "00112233445566778899aabbccddeeff");
    }

    #[test]
    fn sequence_prefixes_and_increments() {
        let s = Sequence::new("req");
        for want in ["req-1", "req-2", "req-3"] {
            assert_eq!(s.next(), want);
        }
    }

    #[test]
    fn sequence_default_prefix() {
        assert_eq!(Sequence::new("").next(), "id-1");
    }

    #[test]
    fn sequence_reset() {
        let s = Sequence::new("x");
        s.next();
        s.next();
        s.reset();
        assert_eq!(s.next(), "x-1");
    }

    #[test]
    fn sequence_concurrent_next_produces_unique_ids() {
        let s = Arc::new(Sequence::new("c"));
        let handles: Vec<_> = (0..16)
            .map(|_| {
                let s = Arc::clone(&s);
                std::thread::spawn(move || (0..100).map(|_| s.next()).collect::<Vec<_>>())
            })
            .collect();
        let mut seen = HashSet::new();
        for h in handles {
            for id in h.join().expect("thread") {
                assert!(seen.insert(id.clone()), "duplicate id {id:?} under concurrent next");
            }
        }
        assert_eq!(seen.len(), 1600);
    }

    #[test]
    fn generators_are_trait_objects() {
        let gens: Vec<Box<dyn IdGen>> = vec![Box::new(default_generator()), Box::new(Sequence::new("a"))];
        assert_eq!(gens.len(), 2);
    }
}
