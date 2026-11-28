use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};

use jiff::Timestamp;

use super::prefs::*;
use super::*;
use crate::fs::{DirEntry, FileInfo, FileType, RootedFs};

fn s(v: &str) -> Value {
    Value::String(v.to_string())
}

fn dict<const N: usize>(pairs: [(&str, Value); N]) -> Value {
    Value::Dict(pairs.into_iter().map(|(k, v)| (k.to_string(), v)).collect())
}

fn fixtures() -> RootedFs {
    RootedFs::new(Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures"))
}

/// Serves fixed files and listings, and fails reads under `denied` with PermissionDenied.
#[derive(Default)]
struct FakeFs {
    files: BTreeMap<String, Vec<u8>>,
    dirs: BTreeMap<String, Vec<String>>,
    denied: bool,
}

impl FakeFs {
    fn info(len: usize) -> FileInfo {
        FileInfo {
            file_type: FileType::File,
            len: len as u64,
            mode: 0o644,
            modified: Some((1_780_000_000, 5)),
            dev: 0,
            ino: 0,
            ctime: None,
        }
    }
}

impl Fs for FakeFs {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        if self.denied {
            return Err(io::ErrorKind::PermissionDenied.into());
        }
        self.files.get(path.to_str().unwrap_or("")).cloned().ok_or_else(|| io::ErrorKind::NotFound.into())
    }
    fn read_dir(&self, path: &Path) -> io::Result<Vec<DirEntry>> {
        let names = self.dirs.get(path.to_str().unwrap_or("")).ok_or_else(|| io::Error::from(io::ErrorKind::NotFound))?;
        Ok(names.iter().map(|n| DirEntry { name: n.clone(), file_type: FileType::File }).collect())
    }
    fn metadata(&self, path: &Path) -> io::Result<FileInfo> {
        if self.denied {
            return Err(io::ErrorKind::PermissionDenied.into());
        }
        self.files.get(path.to_str().unwrap_or("")).map(|d| Self::info(d.len())).ok_or_else(|| io::ErrorKind::NotFound.into())
    }
    fn symlink_metadata(&self, path: &Path) -> io::Result<FileInfo> {
        self.metadata(path)
    }
    fn read_link(&self, _: &Path) -> io::Result<PathBuf> {
        Err(io::ErrorKind::NotFound.into())
    }
}

const XML: &str = r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Name</key><string>Spectra</string>
  <key>Count</key><integer>42</integer>
  <key>Ratio</key><real>1.25</real>
  <key>Enabled</key><true/>
  <key>Created</key><date>2026-05-28T13:38:24Z</date>
  <key>Payload</key><data>AQID</data>
  <key>Nested</key>
  <dict><key>Value</key><string>ok</string></dict>
  <key>Items</key>
  <array><string>a</string><string>b</string></array>
</dict>
</plist>"#;

fn fake_with(path: &str, data: &[u8]) -> FakeFs {
    FakeFs { files: BTreeMap::from([(path.to_string(), data.to_vec())]), ..FakeFs::default() }
}

#[test]
fn read_path_xml() {
    let fs = fake_with("/tmp/sample.plist", XML.as_bytes());
    let d = read(&fs, "/tmp/sample.plist").expect("read");
    assert_eq!(d.path, "/tmp/sample.plist");
    assert!(d.size > 0 && d.mtime.is_some());
    let created: Timestamp = "2026-05-28T13:38:24Z".parse().expect("ts");
    let want = [
        ("Name", s("Spectra")),
        ("Count", Value::Int(42)),
        ("Ratio", Value::Float(1.25)),
        ("Enabled", Value::Bool(true)),
        ("Created", Value::Date(created)),
        ("Payload", Value::Data(vec![1, 2, 3])),
        ("Nested.Value", s("ok")),
        ("Items", Value::Array(vec![s("a"), s("b")])),
    ];
    for (key, value) in want {
        assert_eq!(d.root.lookup(key), Some(&value), "Lookup({key:?})");
    }
}

#[test]
fn read_path_binary() {
    let d = read(&fixtures(), "/sample-binary.plist").expect("read");
    assert_eq!(d.root.lookup("Name"), Some(&s("Spectra Sample \u{fc}")));
    assert_eq!(d.root.lookup("AutoBackup"), Some(&Value::Bool(true)));
    assert!(d.size > 0);
}

#[test]
fn read_key() {
    let fs = fake_with(
        "/tmp/sample.plist",
        br#"<plist version="1.0"><dict><key>Outer</key><dict><key>Inner</key><string>value</string></dict></dict></plist>"#,
    );
    assert_eq!(super::read_key(&fs, "/tmp/sample.plist", "Outer.Inner").expect("key"), s("value"));
    let err = super::read_key(&fs, "/tmp/sample.plist", "Missing").expect_err("missing");
    assert!(err.is_not_exist(), "{err}");
}

#[test]
fn read_missing_file_is_not_exist_and_garbage_is_a_decode_error() {
    let fs = fake_with("/tmp/bad.plist", b"not a plist");
    assert!(read(&fs, "/tmp/absent.plist").expect_err("absent").is_not_exist());
    let err = read(&fs, "/tmp/bad.plist").expect_err("garbage");
    assert!(matches!(&err, Error::Decode { path, .. } if path == "/tmp/bad.plist"), "{err}");
    assert!(err.to_string().starts_with("decode plist /tmp/bad.plist: "), "{err}");
}

#[test]
fn lookup_prefers_exact_dotted_key() {
    let root = dict([("com.apple.example", s("exact")), ("com", dict([("apple", s("nested"))]))]);
    assert_eq!(root.lookup("com.apple.example"), Some(&s("exact")));
    assert_eq!(root.lookup("com.apple"), Some(&s("nested")));
    assert_eq!(root.lookup("com.apple.example..x"), None);
    assert_eq!(root.lookup(""), Some(&root));
}

#[test]
fn resolve_domain() {
    let fs = FakeFs::default();
    assert_eq!(super::resolve_domain(&fs, "com.apple.TimeMachine", false).expect("r"), "/Library/Preferences/com.apple.TimeMachine.plist");
    assert_eq!(super::resolve_domain(&fs, "/tmp/example.plist", false).expect("r"), "/tmp/example.plist");
    assert_eq!(super::resolve_domain(&fs, " /tmp/../tmp/x/./y.plist ", false).expect("r"), "/tmp/x/y.plist");
    assert!(matches!(super::resolve_domain(&fs, "  ", false), Err(Error::EmptyDomain)));
}

#[test]
fn resolve_current_host_uses_existing_by_host_match() {
    let fs = FakeFs {
        dirs: BTreeMap::from([(
            "/Library/Preferences/ByHost".to_string(),
            vec!["com.apple.Other.X.plist".into(), "com.apple.Spotlight.ABC.plist".into(), "com.apple.Spotlight.DEF.plist".into()],
        )]),
        ..FakeFs::default()
    };
    assert_eq!(
        super::resolve_domain(&fs, "com.apple.Spotlight", true).expect("r"),
        "/Library/Preferences/ByHost/com.apple.Spotlight.ABC.plist"
    );
    // No match, or no ByHost directory at all: the unsuffixed path.
    assert_eq!(super::resolve_domain(&fs, "com.apple.Missing", true).expect("r"), "/Library/Preferences/ByHost/com.apple.Missing.plist");
    assert_eq!(
        super::resolve_domain(&FakeFs::default(), "com.apple.Spotlight", true).expect("r"),
        "/Library/Preferences/ByHost/com.apple.Spotlight.plist"
    );
}

#[test]
fn preference_permission_maps_to_full_disk_access() {
    let denied = FakeFs { denied: true, ..FakeFs::default() };
    let err = read(&denied, "/Library/Preferences/com.apple.TimeMachine.plist").expect_err("denied");
    assert!(matches!(err, Error::NeedsFullDiskAccess { .. }), "{err}");
    assert!(err.to_string().contains("needs full disk access"));
    let err = read(&denied, "/tmp/com.apple.TimeMachine.plist").expect_err("denied");
    assert!(matches!(err, Error::Io { .. }), "{err}");
    let err = read_time_machine_prefs(&denied).expect_err("denied");
    assert!(matches!(err, Error::NeedsFullDiskAccess { .. }), "{err}");
}

#[test]
fn typed_time_machine_prefs() {
    let root = dict([
        ("AutoBackup", Value::Bool(true)),
        ("PreferencesVersion", Value::Int(5)),
        ("RequiresACPower", Value::Bool(true)),
        ("SkipPaths", Value::Array(vec![s("/tmp/skip")])),
        ("Destinations", Value::Array(vec![dict([("Name", s("Backup")), ("ID", Value::Int(7))])])),
    ]);
    let got = time_machine_prefs_from_value(&root);
    assert!(got.auto_backup && got.requires_ac_power && got.preferences_version == 5, "{got:?}");
    assert_eq!(got.skip_paths, vec!["/tmp/skip"]);
    assert_eq!(got.destinations.len(), 1);
    assert_eq!(got.destinations[0]["Name"], s("Backup"));
    assert_eq!(got.destinations[0]["ID"], Value::Int(7));
}

#[test]
fn typed_software_update_and_spotlight_prefs() {
    let now: Timestamp = "2026-05-28T13:38:24Z".parse().expect("ts");
    let su_root = dict([
        ("AutomaticCheckEnabled", Value::Bool(true)),
        ("AutomaticDownload", Value::Bool(true)),
        ("AutomaticallyInstallMacOSUpdates", Value::Bool(false)),
        ("ConfigDataInstall", Value::Bool(true)),
        ("CriticalUpdateInstall", Value::Bool(true)),
        ("LastAttemptSystemVersion", s("15.6.1")),
        ("LastRecommendedMajorOSBundleIdentifier", s("com.apple.InstallAssistant.macOS")),
        ("LastSuccessfulDate", Value::Date(now)),
        ("LastRecommendedUpdatesAvailable", Value::Int(2)),
        ("PrimaryLanguages", Value::Array(vec![s("en-US")])),
    ]);
    let su = software_update_prefs_from_value(&su_root);
    assert!(su.automatic_check_enabled && su.automatic_download && !su.automatically_install_macos_updates, "{su:?}");
    assert_eq!(su.last_attempt_system_version, "15.6.1");
    assert!(!su.last_recommended_major_os_bundle_id.is_empty());
    assert_eq!(su.last_successful_date, Some(now));
    assert_eq!(su.last_full_successful_date, None);
    assert_eq!(su.last_recommended_updates_available, 2);
    assert_eq!(su.primary_languages, vec!["en-US"]);

    let spot_root = dict([
        ("IndexingEnabled", Value::Bool(true)),
        ("MenuItemHidden", Value::Bool(true)),
        ("StoresDisabled", Value::Bool(false)),
        ("Exclusions", Value::Array(vec![s("/Volumes/Build")])),
        ("Volumes", Value::Array(vec![s("/")])),
    ]);
    let spot = spotlight_prefs_from_value(&spot_root);
    assert!(spot.indexing_enabled && spot.menu_item_hidden && !spot.stores_disabled, "{spot:?}");
    assert_eq!(spot.exclusions, vec!["/Volumes/Build"]);
    assert_eq!(spot.volumes, vec!["/"]);
}

/// The values below are what Go's `howett.net/plist` + `plistread` produce for the same two
/// fixture files (recorded with a throwaway Go test: `Root.Any()` and the typed prefs).
#[test]
fn fixtures_decode_like_go() {
    let date: Timestamp = "2026-05-28T13:38:24Z".parse().expect("ts");
    let want_root = dict([
        ("AutoBackup", Value::Bool(true)),
        ("RequiresACPower", Value::Bool(false)),
        ("PreferencesVersion", Value::Int(5)),
        ("Negative", Value::Int(-7)),
        ("Big", Value::Int(9_007_199_254_740_993)),
        ("Ratio", Value::Float(1.25)),
        ("Name", s("Spectra Sample \u{fc}")),
        ("LastSuccessfulDate", Value::Date(date)),
        ("Payload", Value::Data(vec![1, 2, 3])),
        ("com.example.dotted", s("exact")),
        ("com", dict([("example", dict([("nested", s("deep"))]))])),
        ("SkipPaths", Value::Array(vec![s("/tmp/skip"), Value::Int(1), s("/Volumes/Build")])),
        ("Destinations", Value::Array(vec![dict([("Name", s("Backup")), ("ID", Value::Int(7))]), s("not-a-dict")])),
    ]);
    for name in ["sample-xml.plist", "sample-binary.plist"] {
        let d = read(&fixtures(), &format!("/{name}")).expect(name);
        assert_eq!(d.root, want_root, "{name}");
        assert_eq!(d.root.lookup("com.example.dotted"), Some(&s("exact")));
        assert_eq!(d.root.lookup("com.example.nested"), Some(&s("deep")));
        assert_eq!(d.root.lookup("com.example").and_then(|v| v.lookup("nested")), Some(&s("deep")));

        let tm = time_machine_prefs_from_value(&d.root);
        assert_eq!(tm.skip_paths, vec!["/tmp/skip", "/Volumes/Build"], "{name}");
        assert_eq!(tm.destinations, vec![BTreeMap::from([("ID".to_string(), Value::Int(7)), ("Name".to_string(), s("Backup"))])]);
        assert!(tm.auto_backup && !tm.requires_ac_power && tm.preferences_version == 5);
        let su = software_update_prefs_from_value(&d.root);
        assert_eq!(su.last_successful_date, Some(date));
        assert_eq!(su.last_full_successful_date, None);
        assert!(su.recommended_updates.is_empty() && su.primary_languages.is_empty());
    }
}

#[test]
fn integers_beyond_int64_are_an_error() {
    let xml = br#"<plist version="1.0"><dict><key>N</key><integer>18446744073709551615</integer></dict></plist>"#;
    let err = decode(xml).expect_err("overflow");
    assert!(err.to_string().contains("dict[N]: integer 18446744073709551615 overflows int64"), "{err}");
}

#[test]
fn clean_matches_go() {
    for (input, want) in [
        ("", "."),
        ("/", "/"),
        ("a//b/", "a/b"),
        ("/../a", "/a"),
        ("../a/..", ".."),
        ("a/../..", ".."),
        ("./.", "."),
        ("/a/b/../c", "/a/c"),
    ] {
        assert_eq!(clean(input), want, "clean({input:?})");
    }
}

#[test]
fn kinds_match_variants() {
    assert_eq!(Value::Float(1.0).kind(), Kind::Float);
    assert_eq!(dict([]).kind(), Kind::Dict);
}
