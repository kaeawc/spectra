//! Go's slash-path helpers (`path.Clean`, `path.Dir`, and `filepath.Join` on Unix), so cgroup
//! paths are walked and printed as the Go build walks and prints them.

/// `path.Clean`: the shortest equivalent path, by purely lexical processing.
pub fn clean(p: &str) -> String {
    if p.is_empty() {
        return ".".to_string();
    }
    let rooted = p.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for part in p.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| *last != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            _ => parts.push(part),
        }
    }
    let body = parts.join("/");
    match (rooted, body.is_empty()) {
        (true, _) => format!("/{body}"),
        (false, true) => ".".to_string(),
        (false, false) => body,
    }
}

/// `path.Dir`: everything before the last slash, cleaned.
pub fn dir(p: &str) -> String {
    let cut = p.rfind('/').map_or(0, |i| i + 1);
    clean(&p[..cut])
}

/// `filepath.Join` on Unix: the non-empty elements joined with `/`, then cleaned. An empty
/// result stays empty.
pub fn join(elems: &[&str]) -> String {
    let parts: Vec<&str> = elems.iter().copied().filter(|e| !e.is_empty()).collect();
    if parts.is_empty() {
        return String::new();
    }
    clean(&parts.join("/"))
}

#[cfg(test)]
mod tests {
    use super::*;

    // Cases from Go's path.TestClean, TestDir and filepath.TestJoin.
    #[test]
    fn clean_matches_go() {
        for (input, want) in [
            ("", "."),
            ("abc", "abc"),
            ("abc/def", "abc/def"),
            ("..", ".."),
            ("../..", "../.."),
            ("/", "/"),
            ("abc/", "abc"),
            ("/abc/def/", "/abc/def"),
            ("abc//def//ghi", "abc/def/ghi"),
            ("//abc", "/abc"),
            ("abc/./def", "abc/def"),
            ("/./abc/def", "/abc/def"),
            ("abc/../../././../def", "../../def"),
            ("/abc/def/../../..", "/"),
            ("/../abc", "/abc"),
            ("abc/def/../ghi/../jkl", "abc/jkl"),
        ] {
            assert_eq!(clean(input), want, "clean({input:?})");
        }
    }

    #[test]
    fn dir_matches_go() {
        for (input, want) in [("", "."), (".", "."), ("/.", "/"), ("/", "/"), ("x/", "x"), ("abc", "."), ("/a/b", "/a"), ("/a", "/")] {
            assert_eq!(dir(input), want, "dir({input:?})");
        }
    }

    #[test]
    fn join_matches_go() {
        assert_eq!(join(&["a", "b", "c"]), "a/b/c");
        assert_eq!(join(&["", "b"]), "b");
        assert_eq!(join(&["", ""]), "");
        assert_eq!(join(&["/sys/fs/cgroup", "/"]), "/sys/fs/cgroup");
        assert_eq!(join(&["/sys/fs/cgroup", "/svc.slice/a.service", "memory.max"]), "/sys/fs/cgroup/svc.slice/a.service/memory.max");
        assert_eq!(join(&["/proc", "pressure", "cpu"]), "/proc/pressure/cpu");
    }
}
