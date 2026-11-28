//! Process roles, runtimes, owning apps and (macOS) code signing (Go's `internal/classify`).
//!
//! Every running process gets a role (app, helper, system, shell, terminal, AI agent, CLI), a
//! language runtime, its owning `.app` and, on macOS, a code-signing summary, with the evidence
//! for each choice. Per-process signals (paths, argv) are cheap and computed every call; the
//! per-executable work (reading the binary, running `codesign`) is cached by file identity in
//! memory and, when a [`Store`] is given, on disk.

mod buildinfo;
mod bundle;
mod elf;
mod engine;
mod files;
mod gopath;
mod macho;
mod reader;
mod role;
mod runtime;
mod sha256;
mod signing;

use std::fmt;
use std::io;
use std::sync::Arc;

use serde::{Deserialize, Deserializer, Serialize, Serializer};
use spectra_base::fs::Fs;
use spectra_base::gojson::{self, GoEmpty};
use spectra_base::hostos::Kind;
use spectra_base::proc::{CancelToken, Runner};

pub use role::{interpreter_args_script, interpreter_script, npm_package};
pub use runtime::interpreter_runtime;

macro_rules! go_string_enum {
    ($(#[$meta:meta])* $name:ident { $($variant:ident => $text:literal),* $(,)? }) => {
        $(#[$meta])*
        #[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash, PartialOrd, Ord)]
        pub enum $name {
            /// Go's zero value, `""`.
            #[default]
            Unset,
            $($variant,)*
        }

        impl $name {
            pub fn as_str(self) -> &'static str {
                match self {
                    $name::Unset => "",
                    $($name::$variant => $text,)*
                }
            }

            /// The value [`Self::as_str`] names; a name this build does not know is
            /// [`Self::Unset`].
            pub fn from_name(name: &str) -> Self {
                match name {
                    $($text => $name::$variant,)*
                    _ => $name::Unset,
                }
            }
        }

        impl<'de> Deserialize<'de> for $name {
            fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
                let name = std::borrow::Cow::<str>::deserialize(deserializer)?;
                Ok($name::from_name(&name))
            }
        }

        impl fmt::Display for $name {
            fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str(self.as_str())
            }
        }

        impl Serialize for $name {
            fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
                serializer.serialize_str(self.as_str())
            }
        }

        impl GoEmpty for $name {
            fn go_empty(&self) -> bool {
                *self == $name::Unset
            }
        }
    };
}

go_string_enum!(
    /// What a process is for, from the user's point of view (Go's `Role`).
    Role {
        App => "app",
        AppHelper => "app_helper",
        System => "system",
        Shell => "shell",
        Terminal => "terminal",
        AiAgent => "ai_agent",
        Cli => "cli",
    }
);

go_string_enum!(
    /// The language runtime a process executes on (Go's `Runtime`).
    Runtime {
        Electron => "electron",
        Native => "native",
        Rust => "rust",
        Go => "go",
        Jvm => "jvm",
        Node => "node",
        Bun => "bun",
        Deno => "deno",
        Python => "python",
        Ruby => "ruby",
        Shell => "shell",
        Unknown => "unknown",
    }
);

go_string_enum!(
    /// The trust class of an executable's code signature (Go's `SigningStatus`).
    SigningStatus {
        Apple => "apple",
        DeveloperId => "developer_id",
        Adhoc => "adhoc",
        Unsigned => "unsigned",
        Invalid => "invalid",
        Unknown => "unknown",
    }
);

/// The outermost `.app` that owns a process (Go's `App`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct App {
    pub name: String,
    pub bundle_path: String,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub bundle_id: String,
}

/// The code-signing summary of one executable, macOS only (Go's `Signing`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Signing {
    pub status: SigningStatus,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub team_id: String,
    pub hardened_runtime: bool,
    /// True only when a `codesign --verify` step passed.
    pub validated: bool,
}

/// The per-process identity the classifier reads (Go's `Input`).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Input {
    pub pid: i64,
    pub ppid: i64,
    pub uid: i64,
    /// argv[0] base name (comm on Linux); never trusted as a path.
    pub command: String,
    /// Kernel-reported executable; may be empty.
    pub executable_path: String,
    /// The kernel reports the executable as deleted (Linux `(deleted)`).
    pub executable_deleted: bool,
    /// argv joined by spaces.
    pub command_line: String,
    /// The argument vector with its boundaries intact, when known.
    pub argv: Vec<String>,
    /// Go's non-nil `LoadArgv`: when set and `argv` is empty, the classifier may fetch argv
    /// through the loader passed to [`Classifier::classify`] (at most once, and only for an
    /// interpreter).
    pub load_argv: bool,
}

/// One process's classification (Go's `Result`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Classification {
    pub role: Role,
    pub runtime: Runtime,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub app: Option<App>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub signing: Option<Signing>,
    #[serde(skip_serializing_if = "gojson::is_empty")]
    pub evidence: Vec<String>,
    /// What the argv loader returned for this input, when the classifier called it (Go's
    /// `LoadArgv` side effect, which `process` keeps on the process).
    #[serde(skip)]
    pub loaded_argv: Option<Vec<String>>,
}

/// The on-disk cache seam (Go's `Store`; `*cache.ShardedStore` satisfies it there). Keys are
/// already hashed (Go's `cache.Key`).
pub trait Store: Send + Sync {
    fn get(&self, key: &[u8]) -> io::Result<Option<Vec<u8>>>;
    fn put(&self, key: &[u8], data: &[u8]) -> io::Result<()>;
}

/// Default cap on how much of one binary the Rust marker scan reads (Go's
/// `DefaultMaxScanBytes`).
pub const DEFAULT_MAX_SCAN_BYTES: i64 = 32 << 20;

/// What a [`Classifier`] runs against (Go's `Options`). Zero values pick Go's defaults.
#[derive(Clone)]
pub struct Options {
    /// [`Kind::Unknown`] means the host.
    pub os: Kind,
    /// Runs `codesign` (macOS only).
    pub runner: Arc<dyn Runner>,
    /// Reads executables, Info.plists and bundle layout.
    pub fs: Arc<dyn Fs>,
    pub store: Option<Arc<dyn Store>>,
    /// Bounds concurrent cold classifications; 0 means 8.
    pub workers: usize,
    /// Caps the Rust marker scan per binary; 0 or less means [`DEFAULT_MAX_SCAN_BYTES`].
    pub max_scan_bytes: i64,
    /// The procfs root on Linux; empty means `/proc`.
    pub proc_fs: String,
    /// Go's `runtime.GOARCH` for picking a universal binary's slice (`arm64`, `amd64`); empty
    /// means the host's.
    pub arch: String,
}

/// Classification stopped early because the [`CancelToken`] was cancelled (Go's
/// `context.Canceled`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Cancelled;

impl fmt::Display for Cancelled {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("context canceled")
    }
}

impl std::error::Error for Cancelled {}

/// Classifies processes; keeps its in-memory caches for its lifetime (Go's `Classifier`). It
/// is safe to share between threads.
pub struct Classifier {
    engine: engine::Engine,
}

impl Classifier {
    pub fn new(opts: Options) -> Classifier {
        Classifier { engine: engine::Engine::new(opts) }
    }

    /// One [`Classification`] per input, in order. `load_argv(i)` fetches argv for
    /// `inputs[i]`. A cancelled `cancel` stops the bundle and cold phases early: affected
    /// processes get runtime unknown, and [`Cancelled`] is returned alongside the otherwise
    /// complete results.
    pub fn classify(
        &self,
        inputs: &[Input],
        load_argv: &dyn Fn(usize) -> Vec<String>,
        cancel: &CancelToken,
    ) -> (Vec<Classification>, Option<Cancelled>) {
        self.engine.classify(inputs, load_argv, cancel)
    }
}

/// Interactive and login shells, matched on lowercased executable base name.
fn shell_name(name: &str) -> bool {
    matches!(name, "sh" | "bash" | "zsh" | "fish" | "dash" | "ksh" | "mksh" | "tcsh" | "csh" | "nu" | "pwsh" | "elvish")
}

#[cfg(test)]
mod tests;
