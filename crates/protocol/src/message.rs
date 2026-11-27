//! Request and response envelopes.

use std::borrow::Cow;
use std::fmt;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::capabilities::CapabilityManifest;
use crate::errors::{ProtocolError, WireError};
use crate::limits::{MAX_REQUEST_ID_LEN, MAX_TIMEOUT_MS};
use crate::present_raw;

/// The wire version.
pub const VERSION: &str = "v1";

/// An operation name. Unknown but well-formed names are valid envelopes.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
#[serde(transparent)]
pub struct Operation(pub Cow<'static, str>);

impl Operation {
    pub const HEALTH: Operation = Operation(Cow::Borrowed("health"));
    pub const INSPECT: Operation = Operation(Cow::Borrowed("inspect"));
    pub const SNAPSHOT_CREATE: Operation = Operation(Cow::Borrowed("snapshot.create"));

    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// Matches `^[a-z][a-z0-9_.]{0,63}$`.
    pub fn well_formed(&self) -> bool {
        let b = self.0.as_bytes();
        !b.is_empty()
            && b.len() <= 64
            && b[0].is_ascii_lowercase()
            && b[1..].iter().all(|&c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'_' || c == b'.')
    }
}

impl fmt::Display for Operation {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

/// 1–128 ASCII characters from `[A-Za-z0-9._:-]`.
pub(crate) fn valid_request_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= MAX_REQUEST_ID_LEN
        && id.bytes().all(|c| c.is_ascii_alphanumeric() || matches!(c, b'.' | b'_' | b':' | b'-'))
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// A request envelope.
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct Request {
    #[serde(default)]
    pub protocol_version: String,
    #[serde(default)]
    pub request_id: String,
    #[serde(default)]
    pub operation: Operation,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub timeout_ms: i64,
    #[serde(default, deserialize_with = "present_raw", skip_serializing_if = "Option::is_none")]
    pub params: Option<Box<RawValue>>,
}

impl Request {
    pub fn validate(&self) -> Result<(), ProtocolError> {
        if self.protocol_version != VERSION {
            return Err(ProtocolError::version(&self.protocol_version));
        }
        if !valid_request_id(&self.request_id) {
            return Err(ProtocolError::plain("invalid request_id"));
        }
        if !self.operation.well_formed() {
            return Err(ProtocolError::plain("invalid operation"));
        }
        if !(0..=MAX_TIMEOUT_MS).contains(&self.timeout_ms) {
            return Err(ProtocolError::plain("invalid timeout_ms"));
        }
        if let Some(params) = &self.params {
            let obj: Option<serde_json::Map<String, serde_json::Value>> =
                serde_json::from_str(params.get()).map_err(|e| ProtocolError::plain(format!("invalid params: {e}")))?;
            if obj.is_none() {
                return Err(ProtocolError::plain("params must be a JSON object"));
            }
        }
        Ok(())
    }
}

/// A response envelope: exactly one of a non-null `result` or an `error`.
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct Response {
    #[serde(default)]
    pub protocol_version: String,
    #[serde(default)]
    pub request_id: String,
    #[serde(default, deserialize_with = "present_raw", skip_serializing_if = "Option::is_none")]
    pub result: Option<Box<RawValue>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<WireError>,
}

impl Response {
    pub fn validate(&self) -> Result<(), ProtocolError> {
        if self.protocol_version != VERSION {
            return Err(ProtocolError::version(&self.protocol_version));
        }
        if self.result.is_none() == self.error.is_none() {
            return Err(ProtocolError::plain("response must contain exactly one of result or error"));
        }
        if let Some(err) = &self.error {
            return err.validate().map_err(|e| e.context("response error"));
        }
        if !self.result.as_deref().is_some_and(valid_non_null) {
            return Err(ProtocolError::plain("result must be non-null JSON"));
        }
        Ok(())
    }

    /// [`Response::validate`] plus a non-empty request ID equal to `req`'s.
    pub fn validate_for(&self, req: &Request) -> Result<(), ProtocolError> {
        self.validate().map_err(|e| e.context("response"))?;
        if self.request_id.is_empty() || self.request_id != req.request_id {
            return Err(ProtocolError::plain("response request_id does not match request"));
        }
        Ok(())
    }
}

pub(crate) fn valid_non_null(raw: &RawValue) -> bool {
    raw.get().trim() != "null"
}

/// The result of `health`.
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct HealthResult {
    #[serde(default)]
    pub capabilities: CapabilityManifest,
}

/// Params of `inspect`.
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct InspectParams {
    #[serde(default)]
    pub app_paths: Option<Vec<String>>,
}

/// Params of `snapshot.create`.
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct SnapshotCreateParams {
    #[serde(default)]
    pub include_apps: bool,
}
