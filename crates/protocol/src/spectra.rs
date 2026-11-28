//! The `spectra capabilities --json` document.

use std::collections::HashSet;

use serde::{Deserialize, Serialize};

use crate::capabilities::SchemaRef;
use crate::errors::{ErrorCode, ProtocolError};
use crate::message::Operation;
use crate::result::{result_schema_name, supported_result_schema_version};

pub const SCHEMA_CAPABILITIES: &str = "spectra.capabilities";
pub const CAPABILITIES_SCHEMA_VERSION: i64 = 1;
pub const MAX_CAPABILITIES_BYTES: usize = 1 << 20;

pub const INTERFACE_VERSION: &str = "version";
pub const INTERFACE_INSPECT: &str = "inspect";
pub const INTERFACE_SNAPSHOT: &str = "snapshot";
pub const INTERFACE_CAPABILITIES: &str = "capabilities";

pub const OUTPUT_TEXT: &str = "text";
pub const OUTPUT_JSON: &str = "json";

/// One CLI interface: how to call it and what it prints.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct SpectraInterface {
    #[serde(default, deserialize_with = "crate::null_default")]
    pub name: String,
    #[serde(default)]
    pub argv: Option<Vec<String>>,
    #[serde(default, deserialize_with = "crate::null_default")]
    pub output: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result_schema: Option<SchemaRef>,
}

/// The `spectra.capabilities` document.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct SpectraCapabilities {
    #[serde(default, deserialize_with = "crate::null_default")]
    pub schema: SchemaRef,
    #[serde(default, deserialize_with = "crate::null_default")]
    pub spectra_version: String,
    #[serde(default, deserialize_with = "crate::null_default")]
    pub os: String,
    #[serde(default, deserialize_with = "crate::null_default")]
    pub arch: String,
    #[serde(default)]
    pub interfaces: Option<Vec<SpectraInterface>>,
}

/// Matches `^[a-z][a-z0-9_.-]{0,63}$`.
fn valid_interface_name(name: &str) -> bool {
    let b = name.as_bytes();
    !b.is_empty()
        && b.len() <= 64
        && b[0].is_ascii_lowercase()
        && b[1..].iter().all(|&c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, b'_' | b'.' | b'-'))
}

fn incompatible(message: impl AsRef<str>) -> ProtocolError {
    ProtocolError::coded(ErrorCode::INCOMPATIBLE_SPECTRA, format!("incompatible_spectra: {}", message.as_ref()))
}

impl SpectraCapabilities {
    /// Every failure is `incompatible_spectra`: a nonconforming document means an incompatible
    /// Spectra.
    pub fn validate(&self) -> Result<(), ProtocolError> {
        if self.schema.name != SCHEMA_CAPABILITIES {
            return Err(incompatible(format!("schema name {:?}, want {SCHEMA_CAPABILITIES:?}", self.schema.name)));
        }
        if self.schema.version != CAPABILITIES_SCHEMA_VERSION {
            return Err(incompatible(format!("schema version {}, want {CAPABILITIES_SCHEMA_VERSION}", self.schema.version)));
        }
        if self.spectra_version.is_empty() || self.os.is_empty() || self.arch.is_empty() {
            return Err(incompatible("spectra_version, os, and arch are required"));
        }
        let interfaces = self.interfaces.as_deref().unwrap_or_default();
        if interfaces.is_empty() {
            return Err(incompatible("at least one interface is required"));
        }
        let mut seen = HashSet::new();
        for iface in interfaces {
            validate_interface(iface)?;
            if !seen.insert(iface.name.as_str()) {
                return Err(incompatible(format!("duplicate interface {:?}", iface.name)));
            }
        }
        let count = interfaces.iter().filter(|i| i.name == INTERFACE_CAPABILITIES).count();
        let want = SchemaRef { name: SCHEMA_CAPABILITIES.into(), version: CAPABILITIES_SCHEMA_VERSION };
        let ok = self.interface(INTERFACE_CAPABILITIES).is_some_and(|c| c.output == OUTPUT_JSON && c.result_schema.as_ref() == Some(&want));
        if count != 1 || !ok {
            return Err(incompatible("capabilities interface must emit spectra.capabilities version 1 JSON"));
        }
        Ok(())
    }

    pub fn interface(&self, name: &str) -> Option<&SpectraInterface> {
        self.interfaces.iter().flatten().find(|i| i.name == name)
    }

    /// The result schema the interface for `op` declares, checked against what this build
    /// supports.
    pub fn result_schema_for(&self, op: &Operation) -> Result<SchemaRef, ProtocolError> {
        let name = interface_for_operation(op).ok_or_else(|| {
            ProtocolError::coded(
                ErrorCode::UNSUPPORTED_OPERATION,
                format!("unsupported_operation: no Spectra interface for operation {:?}", op.as_str()),
            )
        })?;
        let expected_name = result_schema_name(op).unwrap_or_default();
        let expected_version = supported_result_schema_version(expected_name).unwrap_or_default();
        let schema = self.interface(name).and_then(|i| i.result_schema.as_ref());
        match schema {
            Some(s) if s.name == expected_name && s.version == expected_version => Ok(s.clone()),
            _ => {
                let found = schema.map_or_else(|| "<missing>".to_string(), |s| format!("{}/{}", s.name, s.version));
                Err(incompatible(format!("interface {name:?} result schema {found}, want {expected_name}/{expected_version}")))
            }
        }
    }
}

fn validate_interface(iface: &SpectraInterface) -> Result<(), ProtocolError> {
    if !valid_interface_name(&iface.name) {
        return Err(incompatible(format!("invalid interface name {:?}", iface.name)));
    }
    match iface.output.as_str() {
        OUTPUT_JSON => {
            if !iface.result_schema.as_ref().is_some_and(|s| !s.name.is_empty() && s.version >= 1) {
                return Err(incompatible(format!("JSON interface {:?} requires a named result schema with positive version", iface.name)));
            }
        }
        OUTPUT_TEXT => {
            if iface.result_schema.is_some() {
                return Err(incompatible(format!("text interface {:?} must not declare a result schema", iface.name)));
            }
        }
        other => return Err(incompatible(format!("invalid output {other:?} for interface {:?}", iface.name))),
    }
    Ok(())
}

/// The CLI interface that serves `op`.
pub fn interface_for_operation(op: &Operation) -> Option<&'static str> {
    match op.as_str() {
        "inspect" => Some(INTERFACE_INSPECT),
        "snapshot.create" => Some(INTERFACE_SNAPSHOT),
        _ => None,
    }
}

/// Decodes and validates a capabilities document, rejecting oversized input and trailing data.
pub fn decode_spectra_capabilities(data: &[u8]) -> Result<SpectraCapabilities, ProtocolError> {
    if data.len() > MAX_CAPABILITIES_BYTES {
        return Err(incompatible("capabilities document: message too large"));
    }
    let decoded: SpectraCapabilities = serde_json::from_slice(data).map_err(|e| incompatible(format!("decode capabilities: {e}")))?;
    decoded.validate()?;
    Ok(decoded)
}
