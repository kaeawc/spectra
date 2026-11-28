//! Where an opened report's frame can be read in source. `spectra crash inspect` prints each
//! frame as `image`symbol + offset` (or `image + 0xoffset` without a symbol) and no file or
//! line, so a frame links only when its image is one of the open-source system libraries below:
//! a press searches that library's public repository for the symbol. Nothing guesses a path.

/// A library whose source is public, and where.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Source {
    /// The image as a report names it, without `.dylib`.
    pub image: &'static str,
    /// What the repository is called, for the page.
    pub name: &'static str,
    /// `owner/repo` on GitHub.
    pub repo: &'static str,
}

const fn src(image: &'static str, name: &'static str, repo: &'static str) -> Source {
    Source { image, name, repo }
}

/// The libraries a frame can link to. Anything else gets no link.
pub const SOURCES: &[Source] = &[
    src(
        "libdispatch",
        "libdispatch",
        "apple-oss-distributions/libdispatch",
    ),
    src("libsystem_kernel", "xnu", "apple-oss-distributions/xnu"),
    src(
        "libsystem_pthread",
        "libpthread",
        "apple-oss-distributions/libpthread",
    ),
    src(
        "libsystem_malloc",
        "libmalloc",
        "apple-oss-distributions/libmalloc",
    ),
    src(
        "libsystem_platform",
        "libplatform",
        "apple-oss-distributions/libplatform",
    ),
    src("libsystem_c", "Libc", "apple-oss-distributions/Libc"),
    src("libobjc.A", "objc4", "apple-oss-distributions/objc4"),
    src("dyld", "dyld", "apple-oss-distributions/dyld"),
    src("CoreFoundation", "CF", "apple-oss-distributions/CF"),
    src("libswiftCore", "Swift", "swiftlang/swift"),
    src("libswift_Concurrency", "Swift", "swiftlang/swift"),
    src("libc++abi", "LLVM", "llvm/llvm-project"),
    src("libc++.1", "LLVM", "llvm/llvm-project"),
];

/// One frame, split: the image and, when the report had one, the symbol.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Frame<'a> {
    pub image: &'a str,
    pub symbol: Option<&'a str>,
}

/// `libdispatch.dylib`_dispatch_client_callout + 20` → the image and `_dispatch_client_callout`;
/// `WidgetHostService + 0x1a2c4` → the image alone.
pub fn parse_frame(f: &str) -> Frame<'_> {
    let f = f.trim();
    let Some((image, rest)) = f.split_once('`') else {
        let image = match f.rsplit_once(" + ") {
            Some((i, off)) if off.starts_with("0x") => i,
            _ => f,
        };
        return Frame {
            image: image.trim(),
            symbol: None,
        };
    };
    let symbol = match rest.rsplit_once(" + ") {
        Some((s, off)) if !off.is_empty() && off.bytes().all(|b| b.is_ascii_digit()) => s,
        _ => rest,
    }
    .trim();
    Frame {
        image: image.trim(),
        symbol: (!symbol.is_empty()).then_some(symbol),
    }
}

/// The library an image is, when its source is public. Names match exactly, with or without
/// `.dylib`.
pub fn source_for(image: &str) -> Option<&'static Source> {
    let stem = image.trim();
    let stem = stem.strip_suffix(".dylib").unwrap_or(stem);
    SOURCES.iter().find(|s| s.image == stem)
}

/// What to search for: the symbol's own name, without its arguments, generics or the types it's
/// in (`_assertionFailure(_:_:file:line:flags:)` → `_assertionFailure`, `-[NSObject foo:]` →
/// `foo:`). Nothing for a placeholder or an address.
pub fn search_term(symbol: &str) -> Option<String> {
    let s = symbol.trim();
    let last = match s.strip_prefix("-[").or_else(|| s.strip_prefix("+[")) {
        // The selector.
        Some(objc) => objc.trim_end_matches(']').rsplit(' ').next().unwrap_or(""),
        None => {
            let base = s.split(['(', '<']).next().unwrap_or("");
            base.rsplit([':', '.', ' '])
                .find(|p| !p.is_empty())
                .unwrap_or("")
        }
    };
    let ok = !last.is_empty()
        && !last.starts_with("0x")
        && last.chars().any(|c| c.is_alphanumeric() || c == '_')
        && !last.contains('?');
    ok.then(|| last.to_owned())
}

/// `s`, percent-encoded for a URL's query.
fn encode(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

/// A search of `source`'s repository for `term`.
pub fn search_url(source: &Source, term: &str) -> String {
    format!(
        "https://github.com/search?q={}&type=code",
        encode(&format!("repo:{} {term}", source.repo))
    )
}

/// Where a frame's source can be read: the library and the search, when there is one.
pub fn frame_source(frame: &str) -> Option<(&'static Source, String, String)> {
    let f = parse_frame(frame);
    let source = source_for(f.image)?;
    let term = search_term(f.symbol?)?;
    let url = search_url(source, &term);
    Some((source, term, url))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frames_split_into_image_and_symbol() {
        assert_eq!(
            parse_frame("libdispatch.dylib`_dispatch_client_callout + 20"),
            Frame {
                image: "libdispatch.dylib",
                symbol: Some("_dispatch_client_callout")
            }
        );
        assert_eq!(
            parse_frame("WidgetHostService + 0x1a2c4"),
            Frame {
                image: "WidgetHostService",
                symbol: None
            }
        );
        // A symbol with ` + ` inside keeps it; only a trailing offset goes.
        assert_eq!(
            parse_frame("libswiftCore.dylib`static Int.+ infix(_:_:) + 12").symbol,
            Some("static Int.+ infix(_:_:)")
        );
        assert_eq!(parse_frame("???`").symbol, None);
        assert_eq!(parse_frame("").image, "");
    }

    #[test]
    fn known_libraries_map_to_their_public_repository() {
        let name = |i: &str| source_for(i).map(|s| s.name);
        assert_eq!(name("libdispatch.dylib"), Some("libdispatch"));
        assert_eq!(name("libdispatch"), Some("libdispatch"));
        assert_eq!(name("libsystem_kernel.dylib"), Some("xnu"));
        assert_eq!(name("libsystem_pthread.dylib"), Some("libpthread"));
        assert_eq!(name("libobjc.A.dylib"), Some("objc4"));
        assert_eq!(name("CoreFoundation"), Some("CF"));
        assert_eq!(name("libswiftCore.dylib"), Some("Swift"));
        assert_eq!(
            source_for("libsystem_kernel.dylib").unwrap().repo,
            "apple-oss-distributions/xnu"
        );
    }

    #[test]
    fn unknown_and_near_miss_libraries_get_no_link() {
        for image in [
            "WidgetKitCore",
            "WidgetHostService",
            "AppKit",
            "libdispatchx.dylib",
            "LIBDISPATCH.dylib",
            "libdispatch.dylib.dylib",
            "libsystem_kernel.so",
            "Foundation",
            "???",
            "",
        ] {
            assert_eq!(source_for(image), None, "{image}");
        }
    }

    #[test]
    fn search_terms_are_the_symbols_own_name() {
        let t = |s: &str| search_term(s);
        assert_eq!(t("mach_msg2_trap").as_deref(), Some("mach_msg2_trap"));
        assert_eq!(
            t("_assertionFailure(_:_:file:line:flags:)").as_deref(),
            Some("_assertionFailure")
        );
        assert_eq!(
            t("Swift.Array._checkSubscript(_:wasNativeTypeChecked:)").as_deref(),
            Some("_checkSubscript")
        );
        assert_eq!(
            t("std::__1::basic_string<char>::append").as_deref(),
            Some("basic_string")
        );
        assert_eq!(
            t("-[NSObject(NSObject) doesNotRecognizeSelector:]").as_deref(),
            Some("doesNotRecognizeSelector:")
        );
        assert_eq!(t("__CFRunLoopRun").as_deref(), Some("__CFRunLoopRun"));
        assert_eq!(t("<redacted>"), None);
        assert_eq!(t("0x1a2c4"), None);
        assert_eq!(t("???"), None);
        assert_eq!(t(""), None);
    }

    #[test]
    fn a_frame_is_actionable_only_with_a_known_library_and_a_symbol() {
        let (s, term, url) =
            frame_source("libdispatch.dylib`_dispatch_lane_serial_drain + 744").unwrap();
        assert_eq!(
            (s.name, term.as_str()),
            ("libdispatch", "_dispatch_lane_serial_drain")
        );
        assert_eq!(
            url,
            "https://github.com/search?q=repo%3Aapple-oss-distributions%2Flibdispatch%20_dispatch_lane_serial_drain&type=code"
        );
        // The process's own code, a framework that isn't open, and a known library without a
        // symbol all get nothing.
        assert!(frame_source("WidgetHostService + 0x1a2c4").is_none());
        assert!(frame_source("WidgetKitCore`TimelineCache.entry(for:) + 344").is_none());
        assert!(frame_source("libdispatch.dylib + 0x4410").is_none());
    }
}
