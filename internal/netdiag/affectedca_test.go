package netdiag

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// withAffected swaps the package table for the duration of a test.
func withAffected(t *testing.T, table []AffectedCA) {
	t.Helper()
	old := affectedCAs
	affectedCAs = table
	t.Cleanup(func() { affectedCAs = old })
}

func syntheticEntry(c *x509.Certificate, role string) AffectedCA {
	return AffectedCA{Name: "Synthetic Bad CA", SPKIPin: spkiPin(c), Role: role, Source: "https://example.test/src", AsOf: "2026-10-18"}
}

func TestAffectedCAIntermediateFlagged(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root, rootKey := mintCert(t, "Root", true, now.Add(1000*24*time.Hour), nil, nil)
	inter, interKey := mintCert(t, "Bad Intermediate", true, now.Add(900*24*time.Hour), root, rootKey)
	leaf, _ := mintCert(t, "example.test", false, now.Add(100*24*time.Hour), inter, interKey)
	withAffected(t, []AffectedCA{syntheticEntry(inter, "intermediate")})

	roots := x509.NewCertPool()
	roots.AddCert(root)
	var probe TLSProbe
	analyzeChain(&probe, "example.test", []*x509.Certificate{leaf, inter, root}, roots, now)
	if !probe.CompromisedCA || !strings.Contains(probe.CompromisedCADetail, "Synthetic Bad CA") || !strings.Contains(probe.CompromisedCADetail, "https://example.test/src") {
		t.Fatalf("not flagged: %+v", probe)
	}
	if probe.Chain[0].CompromisedCA || !probe.Chain[1].CompromisedCA || probe.Chain[2].CompromisedCA {
		t.Errorf("per-cert flags wrong: %+v", probe.Chain)
	}
}

func TestAffectedCARootFlaggedEvenWhenTrustFails(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root, rootKey := mintCert(t, "Bad Root", true, now.Add(1000*24*time.Hour), nil, nil)
	leaf, _ := mintCert(t, "example.test", false, now.Add(100*24*time.Hour), root, rootKey)
	withAffected(t, []AffectedCA{syntheticEntry(root, "root")})

	// Empty roots pool: verification fails, flag must still be set.
	var probe TLSProbe
	analyzeChain(&probe, "example.test", []*x509.Certificate{leaf, root}, x509.NewCertPool(), now)
	if probe.TrustValid {
		t.Fatal("expected trust failure")
	}
	if !probe.CompromisedCA || !probe.Chain[1].CompromisedCA {
		t.Fatalf("root not flagged on untrusted chain: %+v", probe)
	}
}

func TestAffectedCAFingerprintMatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root, _ := mintCert(t, "Root", true, now.Add(1000*24*time.Hour), nil, nil)
	sum := sha256.Sum256(root.Raw)
	e := AffectedCA{Name: "FP CA", CertSHA256: strings.ToUpper(hex.EncodeToString(sum[:])), Role: "root"}
	if _, ok := matchAffectedCA([]AffectedCA{e}, root); !ok {
		t.Error("fingerprint (case-insensitive) should match")
	}
}

func TestAffectedCAUnrelatedAndLeafOnlyNotFlagged(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bad, _ := mintCert(t, "Bad", true, now.Add(1000*24*time.Hour), nil, nil)
	root, rootKey := mintCert(t, "Good Root", true, now.Add(1000*24*time.Hour), nil, nil)
	leaf, _ := mintCert(t, "example.test", false, now.Add(100*24*time.Hour), root, rootKey)
	withAffected(t, []AffectedCA{syntheticEntry(bad, "intermediate")})

	roots := x509.NewCertPool()
	roots.AddCert(root)
	var probe TLSProbe
	analyzeChain(&probe, "example.test", []*x509.Certificate{leaf, root}, roots, now)
	if probe.CompromisedCA || probe.CompromisedCADetail != "" {
		t.Errorf("unrelated chain flagged: %+v", probe)
	}

	var leafOnly TLSProbe
	analyzeChain(&leafOnly, "example.test", []*x509.Certificate{leaf}, roots, now)
	if leafOnly.CompromisedCA {
		t.Error("leaf-only chain flagged")
	}
}

func TestAffectedCAFinding(t *testing.T) {
	port := PortDiagnosis{Port: 443, TCP: TCPProbe{OK: true}, TLS: &TLSProbe{OK: true, CompromisedCA: true, CompromisedCADetail: "presented chain includes affected CA: X"}}
	var got *Finding
	for _, f := range portFindings("h.test", port) {
		if f.Severity == "high" {
			f := f
			got = &f
		}
	}
	if got == nil || !strings.Contains(got.Detail, "X") || !strings.Contains(got.Detail, "trust stores") {
		t.Fatalf("finding missing or incomplete: %+v", got)
	}
}

func checkDigest(t *testing.T, name, field string, b []byte, err error) {
	t.Helper()
	if err != nil || len(b) != sha256.Size {
		t.Errorf("%s: bad %s", name, field)
	}
}

func checkAffectedEntry(t *testing.T, e AffectedCA) {
	t.Helper()
	if e.Name == "" || !strings.HasPrefix(e.Source, "https://") {
		t.Errorf("incomplete entry: %+v", e)
	}
	if e.Role != "root" && e.Role != "intermediate" {
		t.Errorf("%s: bad role %q", e.Name, e.Role)
	}
	if e.CertSHA256 == "" && e.SPKIPin == "" {
		t.Errorf("%s: no fingerprint or pin", e.Name)
	}
	if _, err := time.Parse("2006-01-02", e.AsOf); err != nil {
		t.Errorf("%s: as_of: %v", e.Name, err)
	}
	if e.CertSHA256 != "" {
		b, err := hex.DecodeString(e.CertSHA256)
		checkDigest(t, e.Name, "cert_sha256", b, err)
	}
	if e.SPKIPin != "" {
		b, err := base64.StdEncoding.DecodeString(e.SPKIPin)
		checkDigest(t, e.Name, "spki_pin", b, err)
	}
}

func TestShippedAffectedCATable(t *testing.T) {
	if affectedCAsErr != nil {
		t.Fatal(affectedCAsErr)
	}
	if len(affectedCAs) == 0 {
		t.Fatal("empty table")
	}
	seen := map[string]bool{}
	for _, e := range affectedCAs {
		checkAffectedEntry(t, e)
		for _, k := range []string{strings.ToLower(e.CertSHA256), e.SPKIPin} {
			if k == "" {
				continue
			}
			if seen[k] {
				t.Errorf("duplicate key %s", k)
			}
			seen[k] = true
		}
	}
}
