//! Runs spectra-protocol's normative conformance cases (`testdata/conformance`, copied from the
//! Go module) against this crate, the same way the Go module's `conformance_test.go` does.

use serde::Deserialize;
use serde_json::value::RawValue;
use spectra_protocol::{
    CapabilityManifest, ErrorCode, Operation, ProtocolError, Request, Response, VERSION, code_of, decode_result,
    decode_spectra_capabilities, negotiate, request_error_code,
};

const FILES: [(&str, &str); 5] = [
    ("requests.json", include_str!("../testdata/conformance/requests.json")),
    ("responses.json", include_str!("../testdata/conformance/responses.json")),
    ("manifests.json", include_str!("../testdata/conformance/manifests.json")),
    ("results.json", include_str!("../testdata/conformance/results.json")),
    ("spectra_capabilities.json", include_str!("../testdata/conformance/spectra_capabilities.json")),
];

#[derive(Deserialize)]
struct Case {
    name: String,
    kind: String,
    input: Box<RawValue>,
    #[serde(default)]
    request: Option<Box<RawValue>>,
    #[serde(default)]
    operation: Operation,
    valid: bool,
    #[serde(default)]
    error_code: Option<ErrorCode>,
}

fn cases() -> Vec<Case> {
    FILES.iter().flat_map(|(name, raw)| serde_json::from_str::<Vec<Case>>(raw).unwrap_or_else(|e| panic!("{name}: {e}"))).collect()
}

fn decode<T: for<'de> Deserialize<'de>>(raw: &RawValue) -> Result<T, ProtocolError> {
    serde_json::from_str(raw.get()).map_err(|e| ProtocolError { code: None, version_mismatch: false, message: e.to_string() })
}

/// Returns the outcome and, for kinds whose code is checked, the failure.
fn run(tc: &Case) -> Result<(), String> {
    let got: Result<(), ProtocolError> = match tc.kind.as_str() {
        "request" => {
            let got = decode::<Request>(&tc.input).and_then(|r| r.validate());
            if let Some(want) = &tc.error_code {
                if tc.valid {
                    if let Err(e) = &got {
                        return Err(format!("valid envelope: {e}"));
                    }
                    if *want != ErrorCode::UNSUPPORTED_OPERATION {
                        return Err(format!("unexpected downstream code {want}"));
                    }
                } else if request_error_code(got.as_ref().err()).as_ref() != Some(want) {
                    return Err(format!("code {:?}, want {want}", request_error_code(got.as_ref().err())));
                }
            }
            got
        }
        "response" => {
            let req_raw = tc.request.as_deref().ok_or("response case without request")?;
            decode::<Request>(req_raw)
                .and_then(|req| req.validate().map(|_| req))
                .and_then(|req| decode::<Response>(&tc.input).and_then(|resp| resp.validate_for(&req)))
        }
        "manifest" => decode::<CapabilityManifest>(&tc.input).and_then(|m| {
            m.validate()?;
            if !m.protocol_versions.iter().flatten().any(|v| v == VERSION) {
                negotiate(&m, &Operation::INSPECT)?;
            }
            Ok(())
        }),
        "result" => {
            let got = decode::<Response>(&tc.input).and_then(|resp| decode_result(&resp, &tc.operation).map(|_| ()));
            check_code(tc, &got)?;
            got
        }
        "spectra_capabilities" => {
            let got = decode_spectra_capabilities(tc.input.get().as_bytes()).and_then(|caps| {
                if !tc.operation.as_str().is_empty() {
                    caps.result_schema_for(&tc.operation)?;
                }
                Ok(())
            });
            check_code(tc, &got)?;
            got
        }
        other => return Err(format!("unknown kind {other:?}")),
    };
    if got.is_ok() != tc.valid {
        return Err(format!("valid={}, got {got:?}", tc.valid));
    }
    Ok(())
}

fn check_code(tc: &Case, got: &Result<(), ProtocolError>) -> Result<(), String> {
    match (tc.valid, &tc.error_code, got) {
        (false, Some(want), Err(e)) if &code_of(e) != want => Err(format!("code {}, want {want}: {e}", code_of(e))),
        _ => Ok(()),
    }
}

#[test]
fn every_conformance_case_passes() {
    let cases = cases();
    assert!(cases.len() >= 30, "only {} cases", cases.len());
    let failures: Vec<String> = cases.iter().filter_map(|tc| run(tc).err().map(|e| format!("{} [{}]: {e}", tc.name, tc.kind))).collect();
    assert!(failures.is_empty(), "{} of {} cases failed:\n{}", failures.len(), cases.len(), failures.join("\n"));
    for kind in ["request", "response", "manifest", "result", "spectra_capabilities"] {
        assert!(cases.iter().any(|c| c.kind == kind), "no {kind} cases");
    }
}
