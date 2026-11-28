//! Typed views of the system preference plists Spectra collectors read (Go's `plistread/prefs.go`).

use std::collections::BTreeMap;

use jiff::Timestamp;

use super::{Error, Value, read, resolve_domain};
use crate::fs::Fs;

/// The subset of Time Machine preferences Spectra collectors need.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TmPrefs {
    pub auto_backup: bool,
    pub destinations: Vec<BTreeMap<String, Value>>,
    pub skip_paths: Vec<String>,
    pub requires_ac_power: bool,
    pub preferences_version: i64,
}

/// The stable subset of software update preferences. A date that is absent is `None`, where Go
/// has the zero `time.Time`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SoftwareUpdatePrefs {
    pub automatic_check_enabled: bool,
    pub automatic_download: bool,
    pub automatically_install_macos_updates: bool,
    pub config_data_install: bool,
    pub critical_update_install: bool,
    pub last_attempt_system_version: String,
    pub last_recommended_major_os_bundle_id: String,
    pub last_successful_date: Option<Timestamp>,
    pub last_full_successful_date: Option<Timestamp>,
    pub last_recommended_updates_available: i64,
    pub recommended_updates: Vec<BTreeMap<String, Value>>,
    pub primary_languages: Vec<String>,
    pub preferences_version: i64,
}

/// The subset of Spotlight preferences useful to storage and indexing collectors.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SpotlightPrefs {
    pub indexing_enabled: bool,
    pub menu_item_hidden: bool,
    pub stores_disabled: bool,
    pub ordered_items: Vec<BTreeMap<String, Value>>,
    pub exclusions: Vec<String>,
    pub volumes: Vec<String>,
    pub preferences_version: i64,
}

/// Reads `/Library/Preferences/com.apple.TimeMachine.plist`.
pub fn read_time_machine_prefs(fs: &dyn Fs) -> Result<TmPrefs, Error> {
    Ok(time_machine_prefs_from_value(&read_domain(fs, "com.apple.TimeMachine")?))
}

/// Reads `/Library/Preferences/com.apple.SoftwareUpdate.plist`.
pub fn read_software_update_prefs(fs: &dyn Fs) -> Result<SoftwareUpdatePrefs, Error> {
    Ok(software_update_prefs_from_value(&read_domain(fs, "com.apple.SoftwareUpdate")?))
}

/// Reads `/Library/Preferences/com.apple.Spotlight.plist`.
pub fn read_spotlight_prefs(fs: &dyn Fs) -> Result<SpotlightPrefs, Error> {
    Ok(spotlight_prefs_from_value(&read_domain(fs, "com.apple.Spotlight")?))
}

fn read_domain(fs: &dyn Fs, domain: &str) -> Result<Value, Error> {
    let path = resolve_domain(fs, domain, false)?;
    Ok(read(fs, &path)?.root)
}

pub fn time_machine_prefs_from_value(root: &Value) -> TmPrefs {
    TmPrefs {
        auto_backup: bool_key(root, &["AutoBackup", "AutoBackupEnabled"]),
        destinations: dict_array_key(root, &["Destinations"]),
        skip_paths: string_array_key(root, &["SkipPaths", "ExcludeByPath", "ExcludedPaths"]),
        requires_ac_power: bool_key(root, &["RequiresACPower"]),
        preferences_version: int_key(root, &["PreferencesVersion"]),
    }
}

pub fn software_update_prefs_from_value(root: &Value) -> SoftwareUpdatePrefs {
    SoftwareUpdatePrefs {
        automatic_check_enabled: bool_key(root, &["AutomaticCheckEnabled"]),
        automatic_download: bool_key(root, &["AutomaticDownload"]),
        automatically_install_macos_updates: bool_key(root, &["AutomaticallyInstallMacOSUpdates"]),
        config_data_install: bool_key(root, &["ConfigDataInstall"]),
        critical_update_install: bool_key(root, &["CriticalUpdateInstall"]),
        last_attempt_system_version: string_key(root, &["LastAttemptSystemVersion"]),
        last_recommended_major_os_bundle_id: string_key(root, &["LastRecommendedMajorOSBundleIdentifier"]),
        last_successful_date: date_key(root, &["LastSuccessfulDate", "LastSuccessfulDateCheck"]),
        last_full_successful_date: date_key(root, &["LastFullSuccessfulDate", "LastFullSuccessfulDateCheck"]),
        last_recommended_updates_available: int_key(root, &["LastRecommendedUpdatesAvailable"]),
        recommended_updates: dict_array_key(root, &["RecommendedUpdates"]),
        primary_languages: string_array_key(root, &["PrimaryLanguages"]),
        preferences_version: int_key(root, &["PreferencesVersion"]),
    }
}

pub fn spotlight_prefs_from_value(root: &Value) -> SpotlightPrefs {
    SpotlightPrefs {
        indexing_enabled: bool_key(root, &["IndexingEnabled", "Enabled"]),
        menu_item_hidden: bool_key(root, &["MenuItemHidden"]),
        stores_disabled: bool_key(root, &["StoresDisabled"]),
        ordered_items: dict_array_key(root, &["orderedItems", "OrderedItems"]),
        exclusions: string_array_key(root, &["Exclusions", "PrivacyExclusions"]),
        volumes: string_array_key(root, &["Volumes"]),
        preferences_version: int_key(root, &["PreferencesVersion"]),
    }
}

/// The first key that is present, whatever its type (as Go's `firstKey`).
fn first_key<'a>(root: &'a Value, keys: &[&str]) -> Option<&'a Value> {
    keys.iter().find_map(|k| root.lookup(k))
}

fn bool_key(root: &Value, keys: &[&str]) -> bool {
    matches!(first_key(root, keys), Some(Value::Bool(true)))
}

fn string_key(root: &Value, keys: &[&str]) -> String {
    match first_key(root, keys) {
        Some(Value::String(s)) => s.clone(),
        _ => String::new(),
    }
}

fn int_key(root: &Value, keys: &[&str]) -> i64 {
    match first_key(root, keys) {
        Some(Value::Int(i)) => *i,
        _ => 0,
    }
}

fn date_key(root: &Value, keys: &[&str]) -> Option<Timestamp> {
    match first_key(root, keys) {
        Some(Value::Date(d)) => Some(*d),
        _ => None,
    }
}

fn string_array_key(root: &Value, keys: &[&str]) -> Vec<String> {
    match first_key(root, keys) {
        Some(Value::Array(items)) => items.iter().filter_map(|e| if let Value::String(s) = e { Some(s.clone()) } else { None }).collect(),
        _ => Vec::new(),
    }
}

fn dict_array_key(root: &Value, keys: &[&str]) -> Vec<BTreeMap<String, Value>> {
    match first_key(root, keys) {
        Some(Value::Array(items)) => items.iter().filter_map(|e| if let Value::Dict(d) = e { Some(d.clone()) } else { None }).collect(),
        _ => Vec::new(),
    }
}
