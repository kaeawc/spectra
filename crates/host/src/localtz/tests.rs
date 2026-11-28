use std::collections::BTreeMap;
use std::io;
use std::path::{Path, PathBuf};

use jiff::Timestamp;
use spectra_base::env::MapEnv;
use spectra_base::fs::{DirEntry, FileInfo, Fs, OsFs};

use super::*;

/// Files with byte contents at absolute paths.
struct BytesFs(BTreeMap<PathBuf, Vec<u8>>);

impl Fs for BytesFs {
    fn read(&self, path: &Path) -> io::Result<Vec<u8>> {
        self.0.get(path).cloned().ok_or_else(|| io::Error::from_raw_os_error(2))
    }
    fn read_dir(&self, _: &Path) -> io::Result<Vec<DirEntry>> {
        Err(io::Error::from_raw_os_error(2))
    }
    fn metadata(&self, _: &Path) -> io::Result<FileInfo> {
        Err(io::Error::from_raw_os_error(2))
    }
    fn symlink_metadata(&self, _: &Path) -> io::Result<FileInfo> {
        Err(io::Error::from_raw_os_error(2))
    }
    fn read_link(&self, _: &Path) -> io::Result<PathBuf> {
        Err(io::Error::from_raw_os_error(2))
    }
}

/// America/Chicago from the host's tzdata (macOS and Linux both ship it), or `None` to skip.
fn chicago() -> Option<Vec<u8>> {
    OsFs.read(Path::new("/usr/share/zoneinfo/America/Chicago")).ok()
}

fn fs_with(paths: &[&str], data: &[u8]) -> BytesFs {
    BytesFs(paths.iter().map(|p| (PathBuf::from(p), data.to_vec())).collect())
}

/// The UTC offset in seconds a zone gives a winter and a summer instant.
fn offsets(tz: &TimeZone) -> (i32, i32) {
    let winter: Timestamp = "2026-01-15T12:00:00Z".parse().expect("ts");
    let summer: Timestamp = "2026-07-15T12:00:00Z".parse().expect("ts");
    (tz.to_offset(winter).seconds(), tz.to_offset(summer).seconds())
}

const CENTRAL: (i32, i32) = (-6 * 3600, -5 * 3600);

#[test]
fn unset_tz_reads_etc_localtime() {
    let Some(data) = chicago() else { return };
    let fs = fs_with(&["/etc/localtime"], &data);
    assert_eq!(offsets(&local(&MapEnv::default(), &fs)), CENTRAL);
    assert_eq!(offsets(&local(&MapEnv::default(), &BytesFs(BTreeMap::new()))), (0, 0), "no /etc/localtime is UTC");
}

#[test]
fn empty_or_utc_tz_is_utc_without_reading() {
    let Some(data) = chicago() else { return };
    let fs = fs_with(&["/etc/localtime", "/usr/share/zoneinfo/UTC"], &data);
    for tz in ["", "UTC", ":UTC", ":"] {
        assert_eq!(offsets(&local(&MapEnv::new([("TZ", tz)]), &fs)), (0, 0), "TZ={tz:?}");
    }
}

#[test]
fn named_zones_come_from_the_zone_sources_in_order() {
    let Some(data) = chicago() else { return };
    let env = MapEnv::new([("TZ", "America/Chicago")]);
    for dir in ["/usr/share/zoneinfo", "/usr/share/lib/zoneinfo", "/usr/lib/locale/TZ", "/etc/zoneinfo"] {
        let fs = fs_with(&[&format!("{dir}/America/Chicago")], &data);
        assert_eq!(offsets(&local(&env, &fs)), CENTRAL, "{dir}");
    }
    let fs = fs_with(&["/usr/share/zoneinfo/America/Chicago"], &data);
    assert_eq!(offsets(&local(&MapEnv::new([("TZ", ":America/Chicago")]), &fs)), CENTRAL, "a leading colon is dropped");
    assert_eq!(offsets(&local(&MapEnv::new([("TZ", "Nowhere/Missing")]), &fs)), (0, 0), "an unknown zone is UTC");
}

#[test]
fn absolute_tz_names_a_file_and_garbage_is_utc() {
    let Some(data) = chicago() else { return };
    let fs = fs_with(&["/srv/zones/central"], &data);
    assert_eq!(offsets(&local(&MapEnv::new([("TZ", "/srv/zones/central")]), &fs)), CENTRAL);
    assert_eq!(offsets(&local(&MapEnv::new([("TZ", ":/srv/zones/central")]), &fs)), CENTRAL);
    let junk = fs_with(&["/etc/localtime"], b"not a tzif file");
    assert_eq!(offsets(&local(&MapEnv::default(), &junk)), (0, 0));
}
