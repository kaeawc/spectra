//! An app's bundle as `spectra --json <App.app>` describes it: what it's built with, how it's
//! signed, what it may do and has been allowed to do, what it installs and what it stores.

use std::collections::BTreeMap;
use std::time::Duration;

use serde::{Deserialize, Deserializer};

/// Spectra writes `null` for an empty list or map; read it as empty.
pub(crate) fn nullable<'de, D: Deserializer<'de>, T: Default + Deserialize<'de>>(d: D) -> Result<T, D::Error> {
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default, rename_all = "PascalCase")]
pub struct Storage {
    pub application_support: u64,
    pub caches: u64,
    pub containers: u64,
    pub group_containers: u64,
    #[serde(rename = "HTTPStorages")]
    pub http_storages: u64,
    #[serde(rename = "WebKit")]
    pub webkit: u64,
    pub logs: u64,
    pub preferences: u64,
    pub total: u64,
    #[serde(deserialize_with = "nullable")]
    pub paths: Vec<String>,
}

impl Storage {
    /// The places it keeps data, largest first, the empty ones left out.
    pub fn parts(&self) -> Vec<(&'static str, u64)> {
        let mut v: Vec<(&'static str, u64)> = [
            ("Containers", self.containers),
            ("Group containers", self.group_containers),
            ("Application Support", self.application_support),
            ("Caches", self.caches),
            ("HTTP storage", self.http_storages),
            ("WebKit", self.webkit),
            ("Logs", self.logs),
            ("Preferences", self.preferences),
        ]
        .into_iter()
        .filter(|(_, b)| *b > 0)
        .collect();
        v.sort_by_key(|p| std::cmp::Reverse(p.1));
        v
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default, rename_all = "PascalCase")]
pub struct Helpers {
    #[serde(deserialize_with = "nullable")]
    pub helper_apps: Vec<String>,
    #[serde(rename = "XPCServices", deserialize_with = "nullable")]
    pub xpc_services: Vec<String>,
    #[serde(deserialize_with = "nullable")]
    pub plugins: Vec<String>,
}

impl Helpers {
    pub fn is_empty(&self) -> bool {
        self.helper_apps.is_empty() && self.xpc_services.is_empty() && self.plugins.is_empty()
    }
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default, rename_all = "PascalCase")]
pub struct LoginItem {
    pub path: String,
    pub label: String,
    /// `user` or `system`.
    pub scope: String,
    pub daemon: bool,
    pub run_at_load: bool,
    pub keep_alive: bool,
}

#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default, rename_all = "PascalCase")]
pub struct Bundle {
    pub path: String,
    #[serde(rename = "UI")]
    pub ui: String,
    pub runtime: String,
    pub language: String,
    pub confidence: String,
    #[serde(deserialize_with = "nullable")]
    pub signals: Vec<String>,
    #[serde(rename = "BundleID")]
    pub bundle_id: String,
    pub app_version: String,
    pub build_number: String,
    #[serde(deserialize_with = "nullable")]
    pub framework_versions: BTreeMap<String, String>,
    #[serde(deserialize_with = "nullable")]
    pub architectures: Vec<String>,
    pub bundle_size_bytes: u64,
    #[serde(rename = "TeamID")]
    pub team_id: String,
    #[serde(rename = "MASReceipt")]
    pub mas_receipt: bool,
    pub hardened_runtime: bool,
    pub sandboxed: bool,
    #[serde(deserialize_with = "nullable")]
    pub entitlements: Vec<String>,
    #[serde(deserialize_with = "nullable")]
    pub storage: Storage,
    #[serde(deserialize_with = "nullable")]
    pub helpers: Helpers,
    #[serde(deserialize_with = "nullable")]
    pub login_items: Vec<LoginItem>,
    #[serde(deserialize_with = "nullable")]
    pub granted_permissions: Vec<String>,
    pub gatekeeper_status: String,
    pub gatekeeper_source: String,
}

impl Bundle {
    /// What it's built with: `Electron · Node+Chromium · TypeScript/JS`, repeats left out.
    pub fn built_with(&self) -> String {
        let mut parts: Vec<&str> = Vec::new();
        for p in [self.ui.as_str(), self.runtime.as_str(), self.language.as_str()] {
            if !p.is_empty() && !parts.contains(&p) {
                parts.push(p);
            }
        }
        if parts.is_empty() { "Unknown".into() } else { parts.join(" · ") }
    }

    pub fn version(&self) -> String {
        match (self.app_version.is_empty(), self.build_number.is_empty()) {
            (true, _) => "Unknown".into(),
            (false, true) => self.app_version.clone(),
            (false, false) if self.build_number == self.app_version => self.app_version.clone(),
            (false, false) => format!("{} (build {})", self.app_version, self.build_number),
        }
    }
}

/// A privacy permission's name, said plainly: `ScreenCapture` as "Screen recording".
pub fn permission_label(raw: &str) -> String {
    let known = match raw {
        "ScreenCapture" => "Screen recording",
        "Camera" => "Camera",
        "Microphone" => "Microphone",
        "Accessibility" => "Accessibility",
        "ListenEvent" => "Input monitoring",
        "PostEvent" => "Controlling the computer",
        "SystemPolicyAllFiles" => "Full Disk Access",
        "SystemPolicyDownloadsFolder" => "Downloads folder",
        "SystemPolicyDocumentsFolder" => "Documents folder",
        "SystemPolicyDesktopFolder" => "Desktop folder",
        "AddressBook" => "Contacts",
        "Calendar" => "Calendars",
        "Reminders" => "Reminders",
        "Photos" => "Photos",
        "AppleEvents" => "Automation",
        "BluetoothAlways" => "Bluetooth",
        "Location" => "Location",
        _ => return raw.to_owned(),
    };
    known.to_owned()
}

pub fn parse(json: &str) -> Result<Bundle, String> {
    let all: Vec<Bundle> = serde_json::from_str(json).map_err(|e| format!("Couldn't read Spectra's output: {e}"))?;
    all.into_iter().next().ok_or_else(|| "Spectra described no bundle.".to_owned())
}

/// How long reading a bundle may take; a big one with a lot stored takes a few seconds.
pub const DEADLINE: Duration = Duration::from_secs(60);

pub fn args(path: &str) -> Vec<String> {
    vec!["--json".into(), path.into()]
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    /// Shaped like `spectra --json` on an Electron app, with names changed.
    pub const CHAT: &str = r#"[{"Path":"/Applications/Chat.app","UI":"Electron","Runtime":"Node+Chromium","Language":"TypeScript/JS",
        "Packaging":"","Confidence":"high","Signals":["Electron Framework.framework present"],"NativeModules":null,"ObjC":null,"Rust":null,
        "BundleID":"com.example.chat","AppVersion":"4.47.72","BuildNumber":"447000072","ElectronVersion":"39.2.7",
        "FrameworkVersions":{"Electron":"39.2.7"},"Architectures":["arm64"],"BundleSizeBytes":247373824,"ApparentSizeBytes":520711104,
        "TeamID":"ABCDE12345","SparkleFeedURL":"","MASReceipt":true,"HardenedRuntime":true,"Sandboxed":true,
        "Entitlements":["app-sandbox","network.client","device.camera"],
        "Storage":{"ApplicationSupport":0,"Caches":1024,"Containers":1448222720,"GroupContainers":0,"HTTPStorages":0,"WebKit":0,"Logs":0,
                   "Preferences":0,"Total":1448223744,"Paths":["/Users/someone/Library/Containers/com.example.chat"]},
        "NetworkEndpoints":null,"PrivacyDescriptions":{"NSCameraUsageDescription":"For video calls."},"Dependencies":null,
        "Helpers":{"HelperApps":["Chat Helper","Chat Helper (GPU)"],"XPCServices":null,"Plugins":null},
        "LoginItems":[{"Path":"/Library/LaunchAgents/com.example.chat.updater.plist","Label":"com.example.chat.updater","Scope":"system",
                       "Daemon":false,"RunAtLoad":true,"KeepAlive":false}],
        "RunningProcesses":[{"PID":681,"RSSKiB":440400,"Command":"Contents/MacOS/Chat","StartTime":"2026-11-20T02:59:16-05:00"}],
        "GrantedPermissions":["Camera","ScreenCapture"],"AppStartedAt":"2026-11-20T02:59:16-05:00","AppUptimeSeconds":10355,
        "GatekeeperStatus":"accepted","GatekeeperSource":"Mac App Store"}]"#;

    /// Shaped like Calculator's: nulls where there's nothing.
    const SPARSE: &str = r#"[{"Path":"/System/Applications/Calc.app","UI":"SwiftUI","Runtime":"Swift","Language":"Swift",
        "Signals":null,"BundleID":"com.example.calc","AppVersion":"12.0","BuildNumber":"225","FrameworkVersions":null,
        "Architectures":["arm64","x86_64"],"Entitlements":null,"Storage":null,"Helpers":null,"LoginItems":null,
        "GrantedPermissions":null,"GatekeeperStatus":"accepted","GatekeeperSource":"Apple System"}]"#;

    #[test]
    fn parses_an_apps_bundle() {
        let b = parse(CHAT).unwrap();
        assert_eq!(b.bundle_id, "com.example.chat");
        assert_eq!(b.built_with(), "Electron · Node+Chromium · TypeScript/JS");
        assert_eq!(b.version(), "4.47.72 (build 447000072)");
        assert_eq!(b.framework_versions.get("Electron").map(String::as_str), Some("39.2.7"));
        assert!(b.hardened_runtime && b.sandboxed && b.mas_receipt);
        assert_eq!(b.helpers.helper_apps.len(), 2);
        assert_eq!(b.login_items[0].label, "com.example.chat.updater");
        assert!(b.login_items[0].run_at_load);
        assert_eq!(b.storage.parts(), [("Containers", 1_448_222_720), ("Caches", 1024)]);
        let granted: Vec<String> = b.granted_permissions.iter().map(|p| permission_label(p)).collect();
        assert_eq!(granted, ["Camera", "Screen recording"]);
    }

    #[test]
    fn nulls_read_as_empty() {
        let b = parse(SPARSE).unwrap();
        assert_eq!(b.built_with(), "SwiftUI · Swift", "the language repeats the runtime");
        assert!(b.entitlements.is_empty() && b.login_items.is_empty() && b.granted_permissions.is_empty());
        assert!(b.helpers.is_empty());
        assert_eq!(b.storage.total, 0);
        assert!(parse("[]").is_err());
        assert!(parse("nope").is_err());
    }

    #[test]
    fn reads_the_bundle_it_is_given() {
        assert_eq!(args("/Applications/Chat.app"), ["--json", "/Applications/Chat.app"]);
    }
}
