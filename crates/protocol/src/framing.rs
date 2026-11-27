//! NDJSON framing: one JSON object per line, with per-direction size limits.

use std::fmt;
use std::io::{self, BufRead, Write};

use serde::Serialize;

use crate::errors::ProtocolError;
use crate::message::{Request, Response};

/// Why a frame could not be read or written.
#[derive(Debug)]
pub enum FramingError {
    /// The line exceeded the limit. The stream position is then undefined: send at most one
    /// `message_too_large` error and close the connection.
    TooLarge,
    Io(io::Error),
    Encode(serde_json::Error),
}

impl fmt::Display for FramingError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            FramingError::TooLarge => f.write_str("message too large"),
            FramingError::Io(e) => write!(f, "read message: {e}"),
            FramingError::Encode(e) => write!(f, "marshal message: {e}"),
        }
    }
}

impl std::error::Error for FramingError {}

/// Reads one line of at most `limit` bytes (excluding the newline). Returns `Ok(None)` at a
/// clean end of stream; a final line without a newline is returned as a message.
pub fn read_message<R: BufRead>(r: &mut R, limit: usize) -> Result<Option<Vec<u8>>, FramingError> {
    let mut line = Vec::with_capacity(limit.saturating_add(1).min(4096));
    loop {
        let buf = r.fill_buf().map_err(FramingError::Io)?;
        if buf.is_empty() {
            return Ok(if line.is_empty() { None } else { Some(line) });
        }
        let (content, newline) = match buf.iter().position(|&b| b == b'\n') {
            Some(i) => (&buf[..i], true),
            None => (buf, false),
        };
        if content.len() > limit - line.len() {
            return Err(FramingError::TooLarge);
        }
        line.extend_from_slice(content);
        let used = content.len() + usize::from(newline);
        r.consume(used);
        if newline {
            return Ok(Some(line));
        }
    }
}

/// Decodes and validates a request line.
pub fn decode_request(line: &[u8]) -> Result<Request, ProtocolError> {
    let req: Request = serde_json::from_slice(line).map_err(|e| ProtocolError::plain(format!("decode request: {e}")))?;
    req.validate().map_err(|e| e.context("validate request"))?;
    Ok(req)
}

/// Decodes and validates a response line.
pub fn decode_response(line: &[u8]) -> Result<Response, ProtocolError> {
    let resp: Response = serde_json::from_slice(line).map_err(|e| ProtocolError::plain(format!("decode response: {e}")))?;
    resp.validate().map_err(|e| e.context("validate response"))?;
    Ok(resp)
}

/// Writes `value` as one line, refusing it when the encoding exceeds `limit`. Uses
/// `serde_json`'s encoding; Spectra itself encodes with `spectra_base::gojson` and calls
/// [`write_encoded`] so its frames match the Go build's bytes.
pub fn write_message<W: Write, T: Serialize + ?Sized>(w: &mut W, value: &T, limit: usize) -> Result<(), FramingError> {
    let encoded = serde_json::to_vec(value).map_err(FramingError::Encode)?;
    write_encoded(w, &encoded, limit)
}

/// Writes already-encoded JSON as one line, refusing it when it exceeds `limit`.
pub fn write_encoded<W: Write>(w: &mut W, encoded: &[u8], limit: usize) -> Result<(), FramingError> {
    if encoded.len() > limit {
        return Err(FramingError::TooLarge);
    }
    let mut frame = Vec::with_capacity(encoded.len() + 1);
    frame.extend_from_slice(encoded);
    frame.push(b'\n');
    w.write_all(&frame).map_err(FramingError::Io)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_lines_and_a_final_line_without_newline() {
        let mut r = io::BufReader::with_capacity(4, &b"{\"a\":1}\n{}"[..]);
        assert_eq!(read_message(&mut r, 64).expect("line"), Some(b"{\"a\":1}".to_vec()));
        assert_eq!(read_message(&mut r, 64).expect("line"), Some(b"{}".to_vec()));
        assert_eq!(read_message(&mut r, 64).expect("eof"), None);
    }

    #[test]
    fn limit_excludes_the_newline() {
        let mut r = io::BufReader::new(&b"abcd\nabcde\n"[..]);
        assert_eq!(read_message(&mut r, 4).expect("fits"), Some(b"abcd".to_vec()));
        assert!(matches!(read_message(&mut r, 4), Err(FramingError::TooLarge)));
    }

    #[test]
    fn write_refuses_oversized_frames() {
        let mut out = Vec::new();
        write_encoded(&mut out, b"{}", 2).expect("fits");
        assert_eq!(out, b"{}\n");
        assert!(matches!(write_encoded(&mut out, b"{\"a\":1}", 2), Err(FramingError::TooLarge)));
    }
}
