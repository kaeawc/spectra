//! Size and time limits of the v1 wire contract.

/// Largest request frame, excluding the newline.
pub const MAX_REQUEST_BYTES: i64 = 1 << 20;
/// Largest response frame, excluding the newline.
pub const MAX_RESPONSE_BYTES: i64 = 16 << 20;
pub const MAX_REQUEST_ID_LEN: usize = 128;
pub const MAX_TIMEOUT_MS: i64 = 600_000;
pub const MAX_ERROR_MESSAGE_BYTES: usize = 4096;
