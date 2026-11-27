//! Checks `spectra_base::gojson` against values written by Go's `encoding/json`
//! (`internal/gojsonvectors`, regenerated with `go test ./internal/gojsonvectors/ -update`).

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use spectra_base::gojson::{self, GoDuration, GoTime, Layout, is_empty, nil_or_empty};

const VECTORS: &str = include_str!("../testdata/gojson-vectors.json");

#[derive(Deserialize)]
struct Vectors {
    float64: Vec<FloatCase>,
    float32: Vec<FloatCase>,
    strings: Vec<StringCase>,
    structs: Vec<StructCase>,
}

#[derive(Deserialize)]
struct FloatCase {
    bits: String,
    want: String,
}

#[derive(Deserialize)]
struct StringCase {
    #[serde(rename = "in")]
    input: String,
    want: String,
}

#[derive(Deserialize)]
struct StructCase {
    name: String,
    compact: String,
    indent: String,
    encoder: String,
}

fn vectors() -> Vectors {
    serde_json::from_str(VECTORS).expect("vectors parse")
}

fn text<T: Serialize + ?Sized>(value: &T) -> String {
    String::from_utf8(gojson::to_vec(value).expect("serialize")).expect("utf8")
}

#[test]
fn float64_matches_go() {
    let cases = vectors().float64;
    assert!(cases.len() > 2000);
    let failures: Vec<String> = cases
        .iter()
        .filter_map(|c| {
            let bits: u64 = c.bits.parse().expect("bits");
            let got = text(&f64::from_bits(bits));
            (got != c.want).then(|| format!("bits {}: got {got}, want {}", c.bits, c.want))
        })
        .collect();
    assert!(failures.is_empty(), "{} mismatches, first: {:?}", failures.len(), &failures[..failures.len().min(5)]);
}

#[test]
fn float32_matches_go() {
    let cases = vectors().float32;
    assert!(cases.len() > 900);
    let failures: Vec<String> = cases
        .iter()
        .filter_map(|c| {
            let bits: u32 = c.bits.parse().expect("bits");
            let got = text(&f32::from_bits(bits));
            (got != c.want).then(|| format!("bits {}: got {got}, want {}", c.bits, c.want))
        })
        .collect();
    assert!(failures.is_empty(), "{} mismatches, first: {:?}", failures.len(), &failures[..failures.len().min(5)]);
}

#[test]
fn strings_match_go() {
    let failures: Vec<String> = vectors()
        .strings
        .iter()
        .filter_map(|c| {
            let got = text(c.input.as_str());
            (got != c.want).then(|| format!("{:?}: got {got}, want {}", c.input, c.want))
        })
        .collect();
    assert!(failures.is_empty(), "{} mismatches, first: {:?}", failures.len(), &failures[..failures.len().min(5)]);
}

// Mirrors `inner` and `sample` in internal/gojsonvectors/vectors_test.go field for field.

#[derive(Serialize, Default)]
struct Inner {
    name: String,
    #[serde(skip_serializing_if = "is_empty")]
    count: i64,
}

#[derive(Serialize, Default)]
struct Sample {
    id: String,
    #[serde(skip_serializing_if = "is_empty")]
    label: String,
    ratio: f64,
    #[serde(skip_serializing_if = "is_empty")]
    small: f32,
    #[serde(skip_serializing_if = "is_empty")]
    enabled: bool,
    tags: Option<Vec<String>>,
    #[serde(skip_serializing_if = "nil_or_empty")]
    extra: Option<Vec<String>>,
    #[serde(skip_serializing_if = "is_empty")]
    ptr: Option<Inner>,
    nested: Inner,
    nested_oe: Inner,
    #[serde(skip_serializing_if = "nil_or_empty")]
    attrs: Option<BTreeMap<String, String>>,
    counts: Option<BTreeMap<String, i64>>,
    at: GoTime,
    elapsed_ns: GoDuration,
    #[serde(skip_serializing_if = "is_empty")]
    note: String,
    #[serde(skip_serializing_if = "GoTime::is_zero")]
    zero: GoTime,
    #[serde(skip_serializing_if = "is_empty")]
    maybe_pct: Option<f64>,
}

fn s(v: &str) -> String {
    v.to_string()
}

fn time(seconds: i64, nanos: i32, offset_seconds: i32) -> GoTime {
    GoTime {
        instant: jiff::Timestamp::new(seconds, nanos).expect("timestamp"),
        offset: jiff::tz::Offset::from_seconds(offset_seconds).expect("offset"),
    }
}

const IST: i32 = 5 * 3600 + 1800;

fn samples() -> Vec<(&'static str, Sample)> {
    vec![
        ("zero", Sample::default()),
        (
            "full",
            Sample {
                id: s("id-1"),
                label: s("Label <b>&</b>"),
                ratio: 0.75,
                small: 1.5,
                enabled: true,
                tags: Some(vec![s("a"), s("b")]),
                extra: Some(vec![s("x")]),
                ptr: Some(Inner { name: s("p"), count: 2 }),
                nested: Inner { name: s("n"), count: 3 },
                nested_oe: Inner { name: s("oe"), count: 0 },
                attrs: Some(BTreeMap::from([(s("zeta"), s("z")), (s("alpha"), s("a")), (s("<k>"), s("v&"))])),
                counts: Some(BTreeMap::from([(s("b"), 2), (s("a"), 1), (s("10"), 10), (s("9"), 9)])),
                // 2026-11-27T13:15:00.12Z
                at: time(1_795_785_300, 120_000_000, 0),
                elapsed_ns: GoDuration(1_500_000_000),
                note: s("n"),
                // 2026-01-02T03:04:05.000000006+05:30
                zero: time(1_767_303_245, 6, IST),
                maybe_pct: Some(0.0),
            },
        ),
        (
            "empty_not_nil",
            Sample {
                id: s("id-2"),
                ratio: 1e21,
                tags: Some(vec![]),
                extra: Some(vec![]),
                attrs: Some(BTreeMap::new()),
                counts: Some(BTreeMap::new()),
                // 2026-11-27T18:45:30+05:30
                at: time(1_795_785_330, 0, IST),
                elapsed_ns: GoDuration(-1),
                ..Sample::default()
            },
        ),
    ]
}

#[test]
fn structs_match_go() {
    let cases = vectors().structs;
    let samples = samples();
    assert_eq!(cases.len(), samples.len());
    for (case, (name, sample)) in cases.iter().zip(&samples) {
        assert_eq!(&case.name, name);
        assert_eq!(text(sample), case.compact, "{name}: compact");
        let indent = String::from_utf8(gojson::to_vec_indent(sample).expect("indent")).expect("utf8");
        assert_eq!(indent, case.indent, "{name}: indent");
        let mut enc = Vec::new();
        gojson::to_writer_encoder(&mut enc, sample, Layout::Compact).expect("encoder");
        assert_eq!(String::from_utf8(enc).expect("utf8"), case.encoder, "{name}: encoder");
    }
}
