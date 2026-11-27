# Conformance fixtures

Copied unchanged from `github.com/kaeawc/spectra-protocol` v0.3.0
(`protocol/v1/conformance/testdata`), MIT licensed. These cases are normative for both
implementations: `tests/conformance.rs` runs every one against this crate, and
`internal/protocolvectors` (Go) fails when these copies drift from the module version in
`go.mod`. When `go.mod` moves to a newer spectra-protocol, copy the new files here.
