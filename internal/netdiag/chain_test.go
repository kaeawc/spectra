package netdiag

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// mintCert issues a certificate for cn signed by parent (self-signed when
// parent/parentKey are nil), valid until notAfter.
func mintCert(t *testing.T, cn string, isCA bool, notAfter time.Time, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		DNSNames:              []string{"example.test"},
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsecert: %v", err)
	}
	return cert, key
}

func TestAnalyzeChainValid(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ca, caKey := mintCert(t, "Test Root CA", true, now.Add(3650*24*time.Hour), nil, nil)
	leaf, _ := mintCert(t, "example.test", false, now.Add(200*24*time.Hour), ca, caKey)

	roots := x509.NewCertPool()
	roots.AddCert(ca)

	var probe TLSProbe
	analyzeChain(&probe, "example.test", []*x509.Certificate{leaf, ca}, roots, now)

	if !probe.TrustValid {
		t.Fatalf("expected trust valid, got error %q", probe.TrustError)
	}
	if len(probe.Chain) != 2 {
		t.Fatalf("chain len = %d, want 2", len(probe.Chain))
	}
	if probe.LeafSPKIPin == "" || probe.Chain[0].SPKIPin == "" {
		t.Error("expected SPKI pins to be populated")
	}
	if probe.KeyType != "ECDSA" || probe.KeyBits != 256 {
		t.Errorf("key = %s/%d, want ECDSA/256", probe.KeyType, probe.KeyBits)
	}
	if probe.Intercepted {
		t.Errorf("did not expect interception: %s", probe.InterceptionReason)
	}
	if probe.ExpiringSoon {
		t.Error("200-day leaf should not be expiring soon")
	}
	if probe.LeafExpiresInDays < 199 || probe.LeafExpiresInDays > 200 {
		t.Errorf("leaf expiry days = %d, want ~200", probe.LeafExpiresInDays)
	}
}

func TestAnalyzeChainSelfSignedIntercepted(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	leaf, _ := mintCert(t, "example.test", false, now.Add(100*24*time.Hour), nil, nil)

	var probe TLSProbe
	analyzeChain(&probe, "example.test", []*x509.Certificate{leaf}, x509.NewCertPool(), now)

	if probe.TrustValid {
		t.Error("self-signed leaf against empty roots must not validate")
	}
	if !probe.Intercepted || probe.InterceptionReason != "leaf certificate is self-signed" {
		t.Errorf("expected self-signed interception, got %v / %q", probe.Intercepted, probe.InterceptionReason)
	}
}

func TestAnalyzeChainVendorIntercepted(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ca, caKey := mintCert(t, "Zscaler Root CA", true, now.Add(3650*24*time.Hour), nil, nil)
	leaf, _ := mintCert(t, "example.test", false, now.Add(100*24*time.Hour), ca, caKey)

	var probe TLSProbe
	analyzeChain(&probe, "example.test", []*x509.Certificate{leaf, ca}, x509.NewCertPool(), now)

	if !probe.Intercepted {
		t.Fatal("expected interception flagged for Zscaler issuer")
	}
	if got := probe.InterceptionReason; got != "issuer matches known interception vendor: zscaler" {
		t.Errorf("reason = %q", got)
	}
}

func TestAnalyzeChainExpiringSoon(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ca, caKey := mintCert(t, "Test Root CA", true, now.Add(3650*24*time.Hour), nil, nil)
	leaf, _ := mintCert(t, "example.test", false, now.Add(10*24*time.Hour), ca, caKey)

	roots := x509.NewCertPool()
	roots.AddCert(ca)

	var probe TLSProbe
	analyzeChain(&probe, "example.test", []*x509.Certificate{leaf, ca}, roots, now)

	if !probe.ExpiringSoon {
		t.Errorf("10-day leaf should be expiring soon (days=%d)", probe.LeafExpiresInDays)
	}
	if probe.TrustValid != true {
		t.Errorf("expiring-but-trusted leaf should still validate: %s", probe.TrustError)
	}
}
