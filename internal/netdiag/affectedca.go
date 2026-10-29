package netdiag

import (
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// AffectedCA is one certificate known to be affected by a CA key compromise.
// Entries match a presented certificate by its SHA-256 fingerprint
// (CertSHA256, hex) and/or its SubjectPublicKeyInfo pin (SPKIPin, base64, the
// same format as spkiPin). At least one must be set. Only values read from a
// primary source belong here; Source records where.
type AffectedCA struct {
	Name       string `json:"name"`
	CertSHA256 string `json:"cert_sha256,omitempty"`
	SPKIPin    string `json:"spki_pin,omitempty"`
	Role       string `json:"role"`
	Source     string `json:"source"`
	AsOf       string `json:"as_of"`
}

//go:embed affected_cas.json
var affectedCAsJSON []byte

// affectedCAs is the table consulted by analyzeChain. It is a variable so
// tests can register synthetic CAs.
var affectedCAs, affectedCAsErr = parseAffectedCAs(affectedCAsJSON)

func parseAffectedCAs(data []byte) ([]AffectedCA, error) {
	var out []AffectedCA
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse affected CA table: %w", err)
	}
	return out, nil
}

// matchAffectedCA returns the table entry matching cert, if any.
func matchAffectedCA(table []AffectedCA, cert *x509.Certificate) (AffectedCA, bool) {
	sum := sha256.Sum256(cert.Raw)
	fp := hex.EncodeToString(sum[:])
	pin := spkiPin(cert)
	for _, e := range table {
		if e.CertSHA256 != "" && strings.EqualFold(e.CertSHA256, fp) {
			return e, true
		}
		if e.SPKIPin != "" && e.SPKIPin == pin {
			return e, true
		}
	}
	return AffectedCA{}, false
}

// flagAffectedCAs marks every certificate in the presented chain (leaf,
// intermediates and root) that matches table. It is independent of trust
// verification: a chain that fails to validate is still reported.
func flagAffectedCAs(probe *TLSProbe, certs []*x509.Certificate, table []AffectedCA) {
	var details []string
	for i, c := range certs {
		e, ok := matchAffectedCA(table, c)
		if !ok {
			continue
		}
		if i < len(probe.Chain) {
			probe.Chain[i].CompromisedCA = true
		}
		details = append(details, fmt.Sprintf("%s (%s, as of %s, source %s)", e.Name, e.Role, e.AsOf, e.Source))
	}
	if len(details) == 0 {
		return
	}
	probe.CompromisedCA = true
	probe.CompromisedCADetail = "presented chain includes affected CA: " + strings.Join(details, "; ")
}
