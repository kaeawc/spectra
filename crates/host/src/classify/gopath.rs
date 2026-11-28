//! Go's `path/filepath` helpers on Unix that the classifier uses, on `&str` so paths print as
//! Go prints them.

pub use crate::pressure::gopath::{clean, dir, join};

/// `filepath.Base`.
pub fn base(p: &str) -> String {
    if p.is_empty() {
        return ".".into();
    }
    let p = p.trim_end_matches('/');
    if p.is_empty() {
        return "/".into();
    }
    p.rsplit('/').next().unwrap_or(p).to_string()
}

/// `filepath.Ext`: the suffix from the final dot of the last element.
pub fn ext(p: &str) -> &str {
    for (i, c) in p.char_indices().rev() {
        match c {
            '/' => return "",
            '.' => return &p[i..],
            _ => {}
        }
    }
    ""
}

/// `filepath.IsAbs`.
pub fn is_abs(p: &str) -> bool {
    p.starts_with('/')
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base_and_ext_match_go() {
        for (p, want) in [("", "."), ("/", "/"), ("/a/b/", "b"), ("a", "a"), ("/x/y.app", "y.app")] {
            assert_eq!(base(p), want, "{p:?}");
        }
        for (p, want) in [("/a/b.app", ".app"), ("/a.app/b", ""), ("x", ""), ("/a/.b", ".b")] {
            assert_eq!(ext(p), want, "{p:?}");
        }
    }
}
