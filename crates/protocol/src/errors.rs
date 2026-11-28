//! Error codes, the wire error object, and the coded error validation returns.

use std::borrow::Cow;
use std::fmt;

use serde::{Deserialize, Serialize};

use crate::limits::MAX_ERROR_MESSAGE_BYTES;

/// A wire error code. Unknown but well-formed codes stay valid for forward compatibility.
#[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
#[serde(transparent)]
pub struct ErrorCode(pub Cow<'static, str>);

impl ErrorCode {
    pub const INVALID_REQUEST: ErrorCode = ErrorCode(Cow::Borrowed("invalid_request"));
    pub const UNSUPPORTED_PROTOCOL_VERSION: ErrorCode = ErrorCode(Cow::Borrowed("unsupported_protocol_version"));
    pub const UNSUPPORTED_OPERATION: ErrorCode = ErrorCode(Cow::Borrowed("unsupported_operation"));
    pub const MESSAGE_TOO_LARGE: ErrorCode = ErrorCode(Cow::Borrowed("message_too_large"));
    pub const PERMISSION_DENIED: ErrorCode = ErrorCode(Cow::Borrowed("permission_denied"));
    pub const UNAVAILABLE: ErrorCode = ErrorCode(Cow::Borrowed("unavailable"));
    pub const INCOMPATIBLE_SPECTRA: ErrorCode = ErrorCode(Cow::Borrowed("incompatible_spectra"));
    pub const TIMEOUT: ErrorCode = ErrorCode(Cow::Borrowed("timeout"));
    pub const EXECUTION_FAILED: ErrorCode = ErrorCode(Cow::Borrowed("execution_failed"));
    pub const INTERNAL: ErrorCode = ErrorCode(Cow::Borrowed("internal"));

    const KNOWN: [ErrorCode; 10] = [
        ErrorCode::INVALID_REQUEST,
        ErrorCode::UNSUPPORTED_PROTOCOL_VERSION,
        ErrorCode::UNSUPPORTED_OPERATION,
        ErrorCode::MESSAGE_TOO_LARGE,
        ErrorCode::PERMISSION_DENIED,
        ErrorCode::UNAVAILABLE,
        ErrorCode::INCOMPATIBLE_SPECTRA,
        ErrorCode::TIMEOUT,
        ErrorCode::EXECUTION_FAILED,
        ErrorCode::INTERNAL,
    ];

    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// One of the codes this version defines.
    pub fn known(&self) -> bool {
        ErrorCode::KNOWN.contains(self)
    }

    /// Matches `^[a-z][a-z0-9_]{0,63}$`.
    pub fn well_formed(&self) -> bool {
        let b = self.0.as_bytes();
        !b.is_empty()
            && b.len() <= 64
            && b[0].is_ascii_lowercase()
            && b[1..].iter().all(|&c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'_')
    }
}

impl fmt::Display for ErrorCode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

/// The `error` object of a response (Go's `v1.Error`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, Default)]
pub struct WireError {
    #[serde(default, deserialize_with = "crate::null_default")]
    pub code: ErrorCode,
    #[serde(default, deserialize_with = "crate::null_default")]
    pub message: String,
}

impl WireError {
    /// A wire error with `message` cut to the limit at a character boundary (Go's `NewError`;
    /// Rust strings are already valid UTF-8, so no repair is needed).
    pub fn new(code: ErrorCode, message: &str) -> WireError {
        let mut end = message.len().min(MAX_ERROR_MESSAGE_BYTES);
        while !message.is_char_boundary(end) {
            end -= 1;
        }
        WireError { code, message: message[..end].to_string() }
    }

    pub fn validate(&self) -> Result<(), ProtocolError> {
        if !self.code.well_formed() {
            return Err(ProtocolError::plain(format!("invalid error code {:?}", self.code.as_str())));
        }
        if self.message.is_empty() || self.message.len() > MAX_ERROR_MESSAGE_BYTES {
            return Err(ProtocolError::plain("invalid error message length"));
        }
        Ok(())
    }
}

/// A validation or decoding failure. `code` is set where the Go module returns a `CodedError`;
/// `version_mismatch` marks the errors Go wraps around `errUnsupportedProtocolVersion`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProtocolError {
    pub code: Option<ErrorCode>,
    pub version_mismatch: bool,
    pub message: String,
}

impl ProtocolError {
    pub(crate) fn plain(message: impl Into<String>) -> Self {
        ProtocolError { code: None, version_mismatch: false, message: message.into() }
    }

    pub(crate) fn coded(code: ErrorCode, message: impl Into<String>) -> Self {
        ProtocolError { code: Some(code), version_mismatch: false, message: message.into() }
    }

    pub(crate) fn version(got: &str) -> Self {
        ProtocolError { code: None, version_mismatch: true, message: format!("protocol version {got:?}: unsupported protocol version") }
    }

    /// Prefixes the message the way Go's `fmt.Errorf("context: %w", err)` does.
    pub(crate) fn context(mut self, context: &str) -> Self {
        self.message = format!("{context}: {}", self.message);
        self
    }
}

impl fmt::Display for ProtocolError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for ProtocolError {}

/// The code a failed request validation maps to (Go's `RequestErrorCode`); `None` for success.
pub fn request_error_code(err: Option<&ProtocolError>) -> Option<ErrorCode> {
    let err = err?;
    Some(if err.version_mismatch { ErrorCode::UNSUPPORTED_PROTOCOL_VERSION } else { ErrorCode::INVALID_REQUEST })
}

/// The error's code, or `internal` when it carries none (Go's `CodeOf`).
pub fn code_of(err: &ProtocolError) -> ErrorCode {
    err.code.clone().unwrap_or(ErrorCode::INTERNAL)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn codes() {
        assert!(ErrorCode::TIMEOUT.known());
        assert!(ErrorCode(Cow::Borrowed("future_code")).well_formed());
        assert!(!ErrorCode(Cow::Borrowed("Bad")).well_formed());
        assert!(!ErrorCode::default().well_formed());
    }

    #[test]
    fn new_error_truncates_at_a_char_boundary() {
        let long = "é".repeat(3000);
        let e = WireError::new(ErrorCode::INTERNAL, &long);
        assert!(e.message.len() <= MAX_ERROR_MESSAGE_BYTES);
        assert_eq!(e.message.len() % 2, 0);
        assert!(e.validate().is_ok());
    }
}
