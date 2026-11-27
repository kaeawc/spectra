//! `spectra <macOS-only command> --json` on Linux prints the same bytes as the Go build
//! (cases from `cmd/spectra/contract_cases_test.go`).

use serde::Deserialize;
use spectra_base::gojson::Encoder;
use spectra_base::hostos::{Kind, Unsupported};

#[derive(Deserialize)]
struct Input {
    command: String,
    feature: String,
    hint: String,
}

#[test]
fn unsupported_reports_match_go() {
    let cases = spectra_contract::cases("unsupported");
    assert_eq!(cases.len(), 19);
    for case in cases {
        let input: Input = case.input();
        let report = Unsupported::new(input.feature, Kind::Linux).command_report(&input.command, &input.hint);
        // The Go CLI writes this with SetIndent("", "  ") and SetEscapeHTML(false), and exits 1.
        let out = Encoder { indent: true, escape_html: false }.encode_to_vec(&report).expect("encode");
        case.assert_stdout(&out);
        assert_eq!(case.expected_exit(), 1, "{}", case.name);
    }
}
