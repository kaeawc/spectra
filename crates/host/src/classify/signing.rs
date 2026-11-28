//! Code signing through `codesign` (Go's `signing.go`, with private copies of
//! `detect.ParseSigningOutput` and `codesignout.OnlyDetritus`).

use std::time::Duration;

use serde::{Deserialize, Serialize};
use spectra_base::gojson;
use spectra_base::hostos::{Kind, Unsupported};
use spectra_base::proc::{CancelToken, Cmd, Output, Runner};

use super::{Signing, SigningStatus};
use crate::goerr;

/// [`Signing`] plus what the role and the evidence line need (Go's `signingVerdict`).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SigningVerdict {
    pub signing: Signing,
    /// Set only for a validated Apple platform binary.
    #[serde(default, skip_serializing_if = "gojson::is_empty")]
    pub platform: bool,
    /// The Apple claim found in the display output; empty when there was none.
    #[serde(default, skip_serializing_if = "gojson::is_empty")]
    pub claim: String,
    /// `--strict` rejected only extended attributes and a plain verify did not pass either.
    #[serde(default, skip_serializing_if = "gojson::is_empty")]
    pub detritus: bool,
    /// `--strict` rejected only Finder metadata but a plain verify passed.
    #[serde(default, skip_serializing_if = "gojson::is_empty")]
    pub finder_metadata: bool,
}

const CODESIGN_TIMEOUT: Duration = Duration::from_secs(10);

/// The code requirement only Apple's own signatures meet.
const APPLE_REQUIREMENT: &str = "anchor apple";

const CLAIM_PLATFORM: &str = "platform binary";

const STRICT_VERIFY: &str = "codesign --verify --strict";

/// codesign --strict's rejection of extended attributes or resource forks.
pub const DETRITUS_COMPLAINT: &str = "resource fork, Finder information, or similar detritus not allowed";

/// The evidence for a signature that --strict rejected only for Finder metadata.
pub const FINDER_METADATA_EVIDENCE: &str = "has Finder metadata (codesign --strict rejects it); code signature verified without --strict";

fn unknown() -> SigningVerdict {
    SigningVerdict { signing: Signing { status: SigningStatus::Unknown, ..Signing::default() }, ..SigningVerdict::default() }
}

fn run(runner: &dyn Runner, cancel: &CancelToken, args: &[&str]) -> Result<Output, String> {
    let cmd = Cmd::new("codesign", args.iter().copied()).timeout(CODESIGN_TIMEOUT).cancel(cancel.clone());
    runner.run(&cmd).map_err(|e| e.to_string())
}

/// Go's `readSigning`: display the signature and, when that claims Apple or shows a third-party
/// Apple-issued certificate, verify it. The error marks a verdict to retry, never to cache.
pub fn read_signing(os: Kind, runner: &dyn Runner, cancel: &CancelToken, exe: &str) -> (SigningVerdict, Option<String>) {
    if os.resolve() != Kind::Darwin {
        return (SigningVerdict::default(), Some(Unsupported::new("codesign inspection", Kind::current()).to_string()));
    }
    let res = match run(runner, cancel, &["-dv", "--verbose=2", exe]) {
        Ok(r) => r,
        Err(e) => return (unknown(), Some(format!("codesign -dv: {e}"))),
    };
    let v = parse_codesign(&format!("{}{}", String::from_utf8_lossy(&res.stderr), String::from_utf8_lossy(&res.stdout)));
    if !v.claim.is_empty() {
        return verify_apple(runner, cancel, exe, v);
    }
    if v.signing.status == SigningStatus::DeveloperId {
        return verify_third_party(runner, cancel, exe, v);
    }
    (v, None)
}

/// A verify that produced a verdict (exit 0, 1 or 3), or why not.
fn verify(runner: &dyn Runner, cancel: &CancelToken, args: &[&str]) -> Result<Output, String> {
    let res = run(runner, cancel, args)?;
    no_verdict(&res)?;
    Ok(res)
}

fn verify_apple(runner: &dyn Runner, cancel: &CancelToken, exe: &str, v: SigningVerdict) -> (SigningVerdict, Option<String>) {
    let req = format!("-R={APPLE_REQUIREMENT}");
    match verify(runner, cancel, &["--verify", "--ignore-resources", &req, exe]) {
        Ok(res) => (apply_verify(v, res.exit_code == 0), None),
        Err(e) => (SigningVerdict { claim: v.claim, ..unknown() }, Some(format!("codesign --verify: {e}"))),
    }
}

fn verify_third_party(runner: &dyn Runner, cancel: &CancelToken, exe: &str, mut v: SigningVerdict) -> (SigningVerdict, Option<String>) {
    let res = match verify(runner, cancel, &["--verify", "--strict", exe]) {
        Ok(r) => r,
        Err(e) => return (v, Some(format!("codesign --verify --strict: {e}"))),
    };
    if res.exit_code == 0 {
        v.signing.validated = true;
    } else if only_detritus(&format!("{}{}", String::from_utf8_lossy(&res.stderr), String::from_utf8_lossy(&res.stdout))) {
        return verify_without_strict(runner, cancel, exe, v);
    } else {
        v.signing.status = SigningStatus::Invalid;
    }
    (v, None)
}

fn verify_without_strict(runner: &dyn Runner, cancel: &CancelToken, exe: &str, mut v: SigningVerdict) -> (SigningVerdict, Option<String>) {
    let res = match verify(runner, cancel, &["--verify", exe]) {
        Ok(r) => r,
        Err(e) => return (v, Some(format!("codesign --verify: {e}"))),
    };
    if res.exit_code == 0 {
        v.signing.validated = true;
        v.finder_metadata = true;
    } else {
        v.detritus = true;
    }
    (v, None)
}

/// Go's `noVerdict`: only exit 0 (passed), 1 (invalid) and 3 (requirement not met) are verdicts.
fn no_verdict(res: &Output) -> Result<(), String> {
    if res.exit_code != 0 && String::from_utf8_lossy(&res.stderr).contains("No such file or directory") {
        return Err("executable vanished during codesign --verify".into());
    }
    match res.exit_code {
        0 | 1 | 3 => Ok(()),
        code => Err(format!("codesign --verify exited {code} without a verdict")),
    }
}

/// Go's `applyVerify`.
fn apply_verify(mut v: SigningVerdict, ok: bool) -> SigningVerdict {
    if ok {
        v.signing.validated = true;
        v.platform = v.claim == CLAIM_PLATFORM;
        v.signing.status = SigningStatus::Apple;
    } else {
        v.signing.status = SigningStatus::Invalid;
    }
    v
}

/// Go's `parseCodesign` over `codesign -dv --verbose=2` output. An Apple claim only sets
/// `claim`; [`apply_verify`] settles it.
pub fn parse_codesign(out: &str) -> SigningVerdict {
    let mut v = SigningVerdict::default();
    (v.signing.team_id, v.signing.hardened_runtime) = parse_signing_output(out);
    if out.contains("is not signed at all") {
        v.signing = Signing { status: SigningStatus::Unsigned, ..Signing::default() };
        return v;
    }
    let mut leaf: Option<&str> = None;
    let (mut adhoc, mut platform) = (false, false);
    for line in out.split('\n') {
        if line.starts_with("Platform identifier=") {
            platform = true;
        } else if line.starts_with("Signature=adhoc") {
            adhoc = true;
        } else if let Some(a) = line.strip_prefix("Authority=")
            && leaf.is_none()
        {
            leaf = Some(a);
        }
    }
    let leaf = leaf.unwrap_or("");
    if platform {
        v.claim = CLAIM_PLATFORM.into();
    } else if leaf == "Software Signing" || leaf.ends_with(" Software Signing") {
        v.claim = "Software Signing leaf".into();
    }
    v.signing.status = if !v.claim.is_empty() {
        SigningStatus::Unknown
    } else if adhoc {
        SigningStatus::Adhoc
    } else if third_party_apple_cert(leaf) {
        SigningStatus::DeveloperId
    } else {
        SigningStatus::Unknown
    };
    v
}

/// Go's `detect.ParseSigningOutput`: the team id and whether the hardened runtime is on.
fn parse_signing_output(out: &str) -> (String, bool) {
    let (mut team, mut hardened) = (String::new(), false);
    for line in out.split('\n') {
        let line = line.trim();
        if let Some(v) = line.strip_prefix("TeamIdentifier=")
            && v != "not set"
        {
            team = v.to_string();
        }
        if has_code_directory_flag(line, "runtime") {
            hardened = true;
        }
    }
    (team, hardened)
}

/// Whether a `flags=0x...(a,b,c)` list on `line` names `flag`.
fn has_code_directory_flag(line: &str, flag: &str) -> bool {
    let Some((_, rest)) = line.split_once("flags=") else { return false };
    let (Some(open), Some(close)) = (rest.find('('), rest.find(')')) else { return false };
    if close < open {
        return false;
    }
    rest[open + 1..close].split(',').any(|f| f == flag)
}

/// Go's `codesignout.OnlyDetritus`: the detritus rejection is the only complaint codesign
/// printed, including in nested "In subcomponent:" sections.
pub fn only_detritus(out: &str) -> bool {
    let mut found = false;
    for line in out.trim().split('\n') {
        let line = line.trim();
        if line.is_empty() || line.starts_with("In subcomponent:") || line.starts_with("In architecture:") {
            continue;
        }
        if line.ends_with(DETRITUS_COMPLAINT) {
            found = true;
        } else {
            return false;
        }
    }
    found
}

/// Go's `signingEvidence`.
pub fn signing_evidence(v: &SigningVerdict) -> String {
    const DISK: &str = "verdict is for the file on disk";
    if !v.claim.is_empty() {
        return format!("{}; {DISK}", apple_evidence(v));
    }
    let mut shown = v.signing.status.as_str().to_string();
    if !v.signing.team_id.is_empty() {
        shown.push_str(&format!(", team {}", v.signing.team_id));
    }
    if v.signing.validated && v.finder_metadata {
        format!("signing: {shown} validated by codesign --verify; {FINDER_METADATA_EVIDENCE}; {DISK}")
    } else if v.signing.validated {
        format!("signing: {shown} validated by {STRICT_VERIFY}; {DISK}")
    } else if v.signing.status == SigningStatus::Invalid {
        format!("signing: codesign -dv displays an Apple-issued certificate but {STRICT_VERIFY} failed; {DISK}")
    } else if v.detritus {
        format!(
            "signing: {shown} as displayed by codesign -dv, not validated: {STRICT_VERIFY} rejected extended attributes or resource forks; {DISK}"
        )
    } else if v.signing.status == SigningStatus::DeveloperId {
        format!("signing: {shown} as displayed by codesign -dv, not validated: {STRICT_VERIFY} could not run; {DISK}")
    } else {
        format!("signing: {shown} as displayed by codesign -dv, not validated; {DISK}")
    }
}

fn apple_evidence(v: &SigningVerdict) -> String {
    let verify = format!("codesign --verify --ignore-resources -R={}", goerr::quote(APPLE_REQUIREMENT));
    if v.signing.validated {
        format!("signing: {} validated by {verify}", v.claim)
    } else if v.signing.status == SigningStatus::Invalid {
        format!("signing: codesign -dv claims {} but {verify} failed", v.claim)
    } else {
        format!("signing: {} claim could not be checked", v.claim)
    }
}

fn third_party_apple_cert(leaf: &str) -> bool {
    [
        "Developer ID Application:",
        "Apple Mac OS Application Signing",
        "Apple Development:",
        "Apple Distribution:",
        "Mac Developer:",
        "3rd Party Mac Developer Application:",
    ]
    .iter()
    .any(|p| leaf.starts_with(p))
}
