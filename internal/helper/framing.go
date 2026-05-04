// Package helper implements the privileged Spectra helper daemon.
// It listens on a Unix socket, authenticates callers via getpeereid,
// and handles a small allowlist of root-only methods.
//
// Wire protocol: 8-byte big-endian message length followed by the JSON
// payload. This makes recovery from partial reads trivial and avoids
// the ambiguity of newline-delimited JSON with embedded newlines.
//
// See docs/design/privileged-helper.md for the full design.
package helper

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxMessageSize = 4 * 1024 * 1024 // 4 MiB safety cap

// WriteMessage writes a length-prefixed message to w.
func WriteMessage(w io.Writer, payload []byte) error {
	if len(payload) > maxMessageSize {
		return fmt.Errorf("helper: message too large (%d > %d)", len(payload), maxMessageSize)
	}
	var hdr [8]byte
	binary.BigEndian.PutUint64(hdr[:], uint64(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("helper: write header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("helper: write payload: %w", err)
	}
	return nil
}

// ReadMessage reads a length-prefixed message from r.
func ReadMessage(r io.Reader) ([]byte, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("helper: read header: %w", err)
	}
	size := binary.BigEndian.Uint64(hdr[:])
	if size > maxMessageSize {
		return nil, fmt.Errorf("helper: message too large (%d > %d)", size, maxMessageSize)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("helper: read payload: %w", err)
	}
	return buf, nil
}
