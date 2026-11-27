//! Diagnostic result envelopes.

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::capabilities::SchemaRef;
use crate::errors::{ErrorCode, ProtocolError};
use crate::message::{HealthResult, Operation, Response, valid_non_null};
use crate::present_raw;
use crate::spectra::SCHEMA_CAPABILITIES;

pub const SCHEMA_INSPECT: &str = "spectra.inspect";
pub const SCHEMA_SNAPSHOT: &str = "spectra.snapshot";

/// `schema`, `spectra_version` and non-null `data`.
#[derive(Clone, Debug, Serialize, Deserialize, Default)]
pub struct DiagnosticResult {
    #[serde(default)]
    pub schema: SchemaRef,
    #[serde(default)]
    pub spectra_version: String,
    #[serde(default, deserialize_with = "present_raw")]
    pub data: Option<Box<RawValue>>,
}

/// The result schema an operation returns.
pub fn result_schema_name(op: &Operation) -> Option<&'static str> {
    match op.as_str() {
        "inspect" => Some(SCHEMA_INSPECT),
        "snapshot.create" => Some(SCHEMA_SNAPSHOT),
        _ => None,
    }
}

/// The schema version this build understands.
pub fn supported_result_schema_version(name: &str) -> Option<i64> {
    match name {
        SCHEMA_INSPECT | SCHEMA_SNAPSHOT | SCHEMA_CAPABILITIES => Some(1),
        _ => None,
    }
}

pub fn check_result_schema(s: &SchemaRef) -> Result<(), ProtocolError> {
    match supported_result_schema_version(&s.name) {
        Some(v) if v == s.version => Ok(()),
        _ => Err(ProtocolError::coded(
            ErrorCode::INCOMPATIBLE_SPECTRA,
            format!("incompatible_spectra: unsupported result schema {:?} version {}", s.name, s.version),
        )),
    }
}

fn success_result(resp: &Response) -> Result<(), ProtocolError> {
    if let Err(e) = resp.validate() {
        return Err(ProtocolError::coded(ErrorCode::INVALID_REQUEST, format!("invalid_request: response: {e}")));
    }
    if resp.error.is_some() {
        return Err(ProtocolError::coded(ErrorCode::INVALID_REQUEST, "invalid_request: response contains an error"));
    }
    Ok(())
}

/// Decodes and checks the diagnostic result of `op` from a success response.
pub fn decode_result(resp: &Response, op: &Operation) -> Result<DiagnosticResult, ProtocolError> {
    success_result(resp)?;
    let expected = result_schema_name(op).ok_or_else(|| {
        ProtocolError::coded(
            ErrorCode::INCOMPATIBLE_SPECTRA,
            format!("incompatible_spectra: no result schema for operation {:?}", op.as_str()),
        )
    })?;
    let raw = resp.result.as_deref().map(RawValue::get).unwrap_or("null");
    let decoded: DiagnosticResult = serde_json::from_str(raw)
        .map_err(|e| ProtocolError::coded(ErrorCode::INVALID_REQUEST, format!("invalid_request: decode result: {e}")))?;
    if decoded.schema.name != expected {
        return Err(ProtocolError::coded(
            ErrorCode::INCOMPATIBLE_SPECTRA,
            format!("incompatible_spectra: schema {:?} does not match operation {:?}", decoded.schema.name, op.as_str()),
        ));
    }
    check_result_schema(&decoded.schema).map_err(|e| e.context("result"))?;
    if !decoded.data.as_deref().is_some_and(valid_non_null) {
        return Err(ProtocolError::coded(ErrorCode::INVALID_REQUEST, "invalid_request: diagnostic data must be non-null JSON"));
    }
    Ok(decoded)
}

/// Decodes a `health` result and validates its manifest.
pub fn decode_health(resp: &Response) -> Result<HealthResult, ProtocolError> {
    success_result(resp)?;
    let raw = resp.result.as_deref().map(RawValue::get).unwrap_or("null");
    let health: HealthResult = serde_json::from_str(raw).map_err(|e| ProtocolError::plain(format!("decode health: {e}")))?;
    health.capabilities.validate().map_err(|e| e.context("health capabilities"))?;
    Ok(health)
}
