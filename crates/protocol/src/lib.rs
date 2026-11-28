//! Spectra's wire contract v1, ported from the Go module `github.com/kaeawc/spectra-protocol`
//! (`protocol/v1`, see its `CONTRACT.md`). Types serialize with the same field names and order
//! as Go; validation rules, limits and error codes are the same, and the Go module's conformance
//! fixtures (`testdata/conformance`) run against this crate.

mod capabilities;
mod errors;
mod framing;
mod limits;
mod message;
mod result;
mod spectra;

pub use capabilities::{CapabilityManifest, Limits, OperationCapability, SchemaRef, negotiate};
pub use errors::{ErrorCode, ProtocolError, WireError, code_of, request_error_code};
pub use framing::{FramingError, decode_request, decode_response, read_message, write_encoded, write_message};
pub use limits::*;
pub use message::{HealthResult, InspectParams, Operation, Request, Response, SnapshotCreateParams, VERSION};
pub use result::{
    DiagnosticResult, SCHEMA_INSPECT, SCHEMA_SNAPSHOT, check_result_schema, decode_health, decode_result, result_schema_name,
    supported_result_schema_version,
};
pub use spectra::{
    CAPABILITIES_SCHEMA_VERSION, INTERFACE_CAPABILITIES, INTERFACE_INSPECT, INTERFACE_SNAPSHOT, INTERFACE_VERSION, MAX_CAPABILITIES_BYTES,
    OUTPUT_JSON, OUTPUT_TEXT, SCHEMA_CAPABILITIES, SpectraCapabilities, SpectraInterface, decode_spectra_capabilities,
    interface_for_operation,
};

/// Deserializes a JSON value that may be `null` into `Some(raw)`, so a present `null` stays
/// distinguishable from a missing field (which `#[serde(default)]` makes `None`), as Go's
/// `json.RawMessage` does.
fn present_raw<'de, D: serde::Deserializer<'de>>(d: D) -> Result<Option<Box<serde_json::value::RawValue>>, D::Error> {
    serde::Deserialize::deserialize(d).map(Some)
}

/// Go's `json.Unmarshal` leaves a non-pointer field at its zero value when the JSON is `null`;
/// `#[serde(default)]` covers only an absent field, so value fields deserialize with this.
fn null_default<'de, D: serde::Deserializer<'de>, T: serde::Deserialize<'de> + Default>(d: D) -> Result<T, D::Error> {
    <Option<T> as serde::Deserialize>::deserialize(d).map(Option::unwrap_or_default)
}
