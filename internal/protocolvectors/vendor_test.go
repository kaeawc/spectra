package protocolvectors

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/kaeawc/spectra-protocol/protocol/v1/conformance"
)

const dir = "../../crates/protocol/testdata/conformance/"

var files = []string{"requests.json", "responses.json", "manifests.json", "results.json", "spectra_capabilities.json"}

// TestRustCopyMatchesModule fails when the Rust crate's fixture copies differ
// from the cases embedded in the spectra-protocol module go.mod requires.
func TestRustCopyMatchesModule(t *testing.T) {
	want, err := conformance.Cases()
	if err != nil {
		t.Fatal(err)
	}
	var got []conformance.Case
	for _, f := range files {
		raw, err := os.ReadFile(dir + f)
		if err != nil {
			t.Fatal(err)
		}
		var cases []conformance.Case
		if err := json.Unmarshal(raw, &cases); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		got = append(got, cases...)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("crates/protocol/testdata/conformance differs from spectra-protocol in go.mod (%d vs %d cases); copy the module's protocol/v1/conformance/testdata files", len(got), len(want))
	}
}
