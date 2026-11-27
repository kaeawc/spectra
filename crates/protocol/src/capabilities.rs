//! Capability manifests and negotiation.

use std::collections::HashSet;

use serde::{Deserialize, Serialize};

use crate::errors::{ErrorCode, ProtocolError};
use crate::limits::{MAX_REQUEST_BYTES, MAX_RESPONSE_BYTES, MAX_TIMEOUT_MS};
use crate::message::{Operation, VERSION};
use crate::result::check_result_schema;

/// A named, versioned result schema.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
pub struct SchemaRef {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub version: i64,
}

/// One operation a peer offers.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct OperationCapability {
    #[serde(default)]
    pub name: Operation,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result_schema: Option<SchemaRef>,
}

/// The limits a peer enforces.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct Limits {
    #[serde(default)]
    pub max_request_bytes: i64,
    #[serde(default)]
    pub max_response_bytes: i64,
    #[serde(default)]
    pub max_timeout_ms: i64,
}

/// What a peer advertises.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct CapabilityManifest {
    #[serde(default)]
    pub protocol_versions: Option<Vec<String>>,
    #[serde(default)]
    pub spectra_version: String,
    #[serde(default)]
    pub agent_version: String,
    #[serde(default)]
    pub operations: Option<Vec<OperationCapability>>,
    #[serde(default)]
    pub limits: Limits,
}

impl CapabilityManifest {
    /// Structural validation. A manifest without `v1` passes here and fails [`negotiate`].
    pub fn validate(&self) -> Result<(), ProtocolError> {
        if self.protocol_versions.as_ref().is_none_or(Vec::is_empty) {
            return Err(ProtocolError::plain("protocol_versions is required"));
        }
        let mut seen = HashSet::new();
        for op in self.operations.iter().flatten() {
            if !op.name.well_formed() {
                return Err(ProtocolError::plain(format!("invalid operation {:?}", op.name.as_str())));
            }
            if !seen.insert(&op.name) {
                return Err(ProtocolError::plain(format!("duplicate operation {:?}", op.name.as_str())));
            }
        }
        let l = &self.limits;
        if !(1..=MAX_REQUEST_BYTES).contains(&l.max_request_bytes) {
            return Err(ProtocolError::plain("invalid max_request_bytes"));
        }
        if !(1..=MAX_RESPONSE_BYTES).contains(&l.max_response_bytes) {
            return Err(ProtocolError::plain("invalid max_response_bytes"));
        }
        if !(1..=MAX_TIMEOUT_MS).contains(&l.max_timeout_ms) {
            return Err(ProtocolError::plain("invalid max_timeout_ms"));
        }
        Ok(())
    }

    pub fn supports(&self, op: &Operation) -> Option<&OperationCapability> {
        self.operations.iter().flatten().find(|c| &c.name == op)
    }
}

/// Checks that `m` speaks v1, offers `op`, and advertises a supported result schema for it.
pub fn negotiate(m: &CapabilityManifest, op: &Operation) -> Result<OperationCapability, ProtocolError> {
    if !m.protocol_versions.iter().flatten().any(|v| v == VERSION) {
        return Err(ProtocolError::coded(
            ErrorCode::UNSUPPORTED_PROTOCOL_VERSION,
            format!("unsupported_protocol_version: protocol {VERSION} unavailable"),
        ));
    }
    let capability = m.supports(op).ok_or_else(|| {
        ProtocolError::coded(ErrorCode::UNSUPPORTED_OPERATION, format!("unsupported_operation: operation {:?} unavailable", op.as_str()))
    })?;
    if let Some(schema) = &capability.result_schema {
        check_result_schema(schema).map_err(|e| e.context("result schema"))?;
    }
    Ok(capability.clone())
}
