package netdiag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func syntheticRoot(t *testing.T, cn string) (AffectedCA, []byte) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c, _ := mintCert(t, cn, true, now.Add(1000*24*time.Hour), nil, nil)
	sum := sha256.Sum256(c.Raw)
	e := AffectedCA{Name: cn, CertSHA256: hex.EncodeToString(sum[:]), Role: "root", Source: "https://example.test/src", AsOf: "2026-11-02"}
	return e, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}

func fakeFiles(files map[string][]byte) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if b, ok := files[p]; ok {
			return b, nil
		}
		return nil, fmt.Errorf("open %s: %w", p, fs.ErrNotExist)
	}
}

func TestTrustStoreLinuxFindsAffectedRoot(t *testing.T) {
	bad, badPEM := syntheticRoot(t, "Bad Root")
	_, otherPEM := syntheticRoot(t, "Other Root")
	bundle := append(append([]byte("# comment\n"), otherPEM...), badPEM...)
	res, err := CheckTrustStore(context.Background(), TrustStoreOptions{
		OS:       hostos.Linux,
		ReadFile: fakeFiles(map[string][]byte{"/etc/pki/tls/certs/ca-bundle.crt": bundle}),
		Table:    []AffectedCA{bad},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Present) != 1 || res.Present[0].Name != "Bad Root" || len(res.Sources) != 1 {
		t.Fatalf("result: %+v", res)
	}
	findings := trustStoreFindings(res)
	if len(findings) != 1 || findings[0].Severity != "high" ||
		findings[0].Title != "the host trust store still contains Bad Root" ||
		!strings.Contains(findings[0].Detail, "Update the OS/browser trust store") ||
		!strings.Contains(findings[0].Detail, "does not mean the host is compromised") {
		t.Fatalf("finding: %+v", findings)
	}
}

func TestTrustStoreLinuxCleanAndIntermediatesIgnored(t *testing.T) {
	bad, _ := syntheticRoot(t, "Bad Root")
	_, otherPEM := syntheticRoot(t, "Other Root")
	inter := bad
	inter.Role = "intermediate"
	res, err := CheckTrustStore(context.Background(), TrustStoreOptions{
		OS:       hostos.Linux,
		ReadFile: fakeFiles(map[string][]byte{"/etc/ssl/certs/ca-certificates.crt": otherPEM}),
		Table:    []AffectedCA{bad, inter},
	})
	if err != nil || len(res.Present) != 0 || len(trustStoreFindings(res)) != 0 {
		t.Fatalf("expected clean: %+v %v", res, err)
	}
}

func TestTrustStoreLinuxErrors(t *testing.T) {
	_, err := CheckTrustStore(context.Background(), TrustStoreOptions{OS: hostos.Linux, ReadFile: fakeFiles(nil)})
	if err == nil || !strings.Contains(err.Error(), "no CA bundle") {
		t.Errorf("missing bundles: %v", err)
	}
	boom := errors.New("boom")
	_, err = CheckTrustStore(context.Background(), TrustStoreOptions{OS: hostos.Linux, ReadFile: func(string) ([]byte, error) { return nil, boom }})
	if !errors.Is(err, boom) {
		t.Errorf("read error not wrapped: %v", err)
	}
}

const securityOutput = `keychain: "/System/Library/Keychains/SystemRootCertificates.keychain"
version: 512
class: 0x80001000
attributes:
    "labl"<blob>="Synthetic Root"
SHA-256 hash: ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB
SHA-1 hash: 0123456789ABCDEF0123456789ABCDEF01234567
keychain: "/System/Library/Keychains/SystemRootCertificates.keychain"
SHA-256 hash: not-a-hash
`

func TestParseSecurityFingerprints(t *testing.T) {
	got := parseSecurityFingerprints([]byte(securityOutput))
	if len(got) != 1 || got[0] != "abababababababababababababababababababababababababababababababab" {
		t.Fatalf("got %v", got)
	}
}

func TestTrustStoreDarwin(t *testing.T) {
	entry := AffectedCA{Name: "Mac Root", CertSHA256: "abababababababababababababababababababababababababababababababab", Role: "root"}
	fake := proc.NewFake().
		On(proc.MatchExact("security", "find-certificate", "-a", "-Z", darwinSystemRoots), proc.Response{Result: proc.Result{Stdout: []byte(securityOutput)}}).
		On(proc.MatchExact("security", "find-certificate", "-a", "-Z"), proc.Response{Result: proc.Result{ExitCode: 44}})
	res, err := CheckTrustStore(context.Background(), TrustStoreOptions{OS: hostos.Darwin, Runner: fake, Table: []AffectedCA{entry}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Present) != 1 || len(res.Sources) != 1 {
		t.Fatalf("result: %+v", res)
	}

	failing := proc.NewFake().On(func(proc.Cmd) bool { return true }, proc.Response{Err: errors.New("no security")})
	if _, err := CheckTrustStore(context.Background(), TrustStoreOptions{OS: hostos.Darwin, Runner: failing, Table: []AffectedCA{entry}}); err == nil {
		t.Error("runner error should propagate")
	}
	empty := proc.NewFake().On(func(proc.Cmd) bool { return true }, proc.Response{Result: proc.Result{ExitCode: 1}})
	if _, err := CheckTrustStore(context.Background(), TrustStoreOptions{OS: hostos.Darwin, Runner: empty, Table: []AffectedCA{entry}}); err == nil {
		t.Error("no certificates should be an error")
	}
}

func TestHostTrustFindings(t *testing.T) {
	ctx := context.Background()
	unsupported := func(context.Context) (TrustStoreResult, error) {
		return TrustStoreResult{}, hostos.Unsupported("trust store check")
	}
	if got := hostTrustFindings(ctx, unsupported); len(got) != 0 {
		t.Errorf("unsupported OS should be silent: %+v", got)
	}
	failed := func(context.Context) (TrustStoreResult, error) { return TrustStoreResult{}, errors.New("nope") }
	if got := hostTrustFindings(ctx, failed); len(got) != 1 || got[0].Severity != "info" {
		t.Errorf("failure should be an info finding: %+v", got)
	}
	hit := func(context.Context) (TrustStoreResult, error) {
		return TrustStoreResult{Present: []AffectedCA{{Name: "X", CertSHA256: "ab"}}}, nil
	}
	if got := hostTrustFindings(ctx, hit); len(got) != 1 || got[0].Severity != "high" {
		t.Errorf("hit should be high: %+v", got)
	}
}

func TestTrustStoreUnsupportedOS(t *testing.T) {
	defer hostos.SetForTest(hostos.Unknown)()
	_, err := CheckTrustStore(context.Background(), TrustStoreOptions{})
	if !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestShippedTableHasEmSignECCRoot(t *testing.T) {
	var found bool
	for _, e := range affectedCAs {
		if e.Role == "root" && e.Name == "emSign ECC Root CA - G3" {
			found = true
			if !strings.HasPrefix(e.Source, "https://ccadb.") {
				t.Errorf("source: %s", e.Source)
			}
		}
	}
	if !found {
		t.Error("emSign ECC Root CA - G3 missing from shipped table")
	}
}
