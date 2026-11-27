// Package gojsonvectors records how Go's encoding/json writes floats, strings and
// structs, so the Rust port's Go-compatible JSON writer (crates/base, module
// gojson) can be checked against Go byte for byte. See docs/design/rust-port.md.
//
// The vectors live in crates/base/testdata/gojson-vectors.json. Regenerate them
// with:
//
//	go test ./internal/gojsonvectors/ -run TestVectors -update
//
// Without -update the test fails when the checked-in file no longer matches what
// this Go toolchain writes.
package gojsonvectors
