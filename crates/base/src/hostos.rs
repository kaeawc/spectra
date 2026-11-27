//! Host-OS selection: anything that shells out or reads OS files picks its binary, path and
//! parser from a [`Kind`]. Collectors take a `Kind` option whose default ([`Kind::Unknown`])
//! resolves to the host, so Linux code paths stay testable on a macOS box.

use std::fmt;

/// The operating system a collector targets.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub enum Kind {
    /// Not chosen; [`Kind::resolve`] turns it into the host's kind.
    #[default]
    Unknown,
    Darwin,
    Linux,
}

impl Kind {
    /// The kind of the OS this binary was built for.
    pub const fn current() -> Kind {
        if cfg!(target_os = "macos") {
            Kind::Darwin
        } else if cfg!(target_os = "linux") {
            Kind::Linux
        } else {
            Kind::Unknown
        }
    }

    /// `self`, or the host's kind when `self` is [`Kind::Unknown`].
    pub const fn resolve(self) -> Kind {
        match self {
            Kind::Unknown => Kind::current(),
            k => k,
        }
    }

    /// The lowercase name Go's `runtime.GOOS` uses, which is also what Spectra prints.
    pub const fn as_str(self) -> &'static str {
        match self {
            Kind::Darwin => "darwin",
            Kind::Linux => "linux",
            Kind::Unknown => "unknown",
        }
    }
}

impl fmt::Display for Kind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// A feature that has no implementation on the OS it was asked to run on, such as `.app`
/// inspection on Linux.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Unsupported {
    pub feature: String,
    pub os: Kind,
}

impl Unsupported {
    /// `feature` is unsupported on `os` (resolved, so `Kind::Unknown` means the host).
    pub fn new(feature: impl Into<String>, os: Kind) -> Self {
        Unsupported { feature: feature.into(), os: os.resolve() }
    }
}

impl fmt::Display for Unsupported {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        // Same text as Go's hostos.UnsupportedError, which ends up in JSON reports.
        write!(f, "{}: unsupported on host OS ({})", self.feature, self.os)
    }
}

impl std::error::Error for Unsupported {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn current_matches_target_os() {
        let want = if cfg!(target_os = "macos") {
            Kind::Darwin
        } else if cfg!(target_os = "linux") {
            Kind::Linux
        } else {
            Kind::Unknown
        };
        assert_eq!(Kind::current(), want);
    }

    #[test]
    fn names_match_go() {
        assert_eq!(Kind::Darwin.to_string(), "darwin");
        assert_eq!(Kind::Linux.to_string(), "linux");
        assert_eq!(Kind::Unknown.to_string(), "unknown");
    }

    #[test]
    fn resolve_keeps_explicit_kinds_and_fills_unknown() {
        assert_eq!(Kind::Linux.resolve(), Kind::Linux);
        assert_eq!(Kind::Darwin.resolve(), Kind::Darwin);
        assert_eq!(Kind::Unknown.resolve(), Kind::current());
        assert_eq!(Kind::default(), Kind::Unknown);
    }

    #[test]
    fn unsupported_message_matches_go() {
        let err = Unsupported::new("inspect", Kind::Linux);
        assert_eq!(err.to_string(), "inspect: unsupported on host OS (linux)");
    }
}
