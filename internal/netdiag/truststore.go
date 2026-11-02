package netdiag

import (
	"context"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"crypto/sha256"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// linuxCABundles are the concatenated PEM bundles the major distribution
// families ship (Debian/Ubuntu, RHEL/Fedora, Alpine/other).
var linuxCABundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/cert.pem",
}

// darwinSystemRoots is the keychain that holds the roots Apple ships; it is
// not in the default keychain search list, so it is queried explicitly.
const darwinSystemRoots = "/System/Library/Keychains/SystemRootCertificates.keychain"

// TrustStoreOptions injects every external dependency of CheckTrustStore.
type TrustStoreOptions struct {
	// OS selects the backend; the zero value resolves to the host.
	OS hostos.Kind
	// ReadFile defaults to os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// Runner defaults to proc.OS{}.
	Runner proc.Runner
	// Table defaults to the shipped affected-CA table. Only root entries
	// with a certificate SHA-256 are consulted.
	Table []AffectedCA
}

// TrustStoreResult reports which affected roots a host trust store contains.
type TrustStoreResult struct {
	// Sources lists the bundle files or keychains that were read.
	Sources []string `json:"sources,omitempty"`
	// Skipped lists bundle files that exist but could not be read.
	Skipped []string `json:"skipped,omitempty"`
	// Present lists affected roots found in the store.
	Present []AffectedCA `json:"present,omitempty"`
	// OS is the resolved host OS the check ran on; it selects the wording
	// and severity of the finding.
	OS hostos.Kind `json:"-"`
}

// trustStoreTimeout bounds the default host check so a hung `security`
// cannot stall Diagnose.
const trustStoreTimeout = 10 * time.Second

// CheckTrustStore reports whether the host trust store still contains a root
// from the affected-CA table. Presence is not trust: a store may carry the
// certificate with trust disabled, which this check does not inspect.
func CheckTrustStore(ctx context.Context, opts TrustStoreOptions) (TrustStoreResult, error) {
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}
	if opts.Runner == nil {
		opts.Runner = proc.OS{}
	}
	if opts.Table == nil {
		opts.Table = affectedCAs
	}
	var (
		fps     map[string]bool
		sources []string
		skipped []string
		err     error
	)
	kind := hostos.Resolve(opts.OS)
	switch kind {
	case hostos.Linux:
		fps, sources, skipped, err = linuxTrustFingerprints(opts.ReadFile)
	case hostos.Darwin:
		fps, sources, err = darwinTrustFingerprints(ctx, opts.Runner)
	default:
		return TrustStoreResult{}, hostos.Unsupported("trust store check")
	}
	if err != nil {
		return TrustStoreResult{}, err
	}
	return TrustStoreResult{Sources: sources, Skipped: skipped, Present: affectedRootsIn(opts.Table, fps), OS: kind}, nil
}

func affectedRootsIn(table []AffectedCA, fps map[string]bool) []AffectedCA {
	var out []AffectedCA
	for _, e := range table {
		if e.Role == "root" && e.CertSHA256 != "" && fps[strings.ToLower(e.CertSHA256)] {
			out = append(out, e)
		}
	}
	return out
}

// linuxTrustFingerprints reads every bundle it can. A bundle that is absent is
// ignored; one that exists but is unreadable (for example EACCES) is recorded
// and skipped. It fails only when no bundle could be read at all.
func linuxTrustFingerprints(readFile func(string) ([]byte, error)) (map[string]bool, []string, []string, error) {
	fps := map[string]bool{}
	var sources, skipped []string
	var firstErr error
	for _, path := range linuxCABundles {
		data, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			skipped = append(skipped, path)
			if firstErr == nil {
				firstErr = fmt.Errorf("read CA bundle %s: %w", path, err)
			}
			continue
		}
		sources = append(sources, path)
		for _, fp := range pemFingerprints(data) {
			fps[fp] = true
		}
	}
	if len(sources) == 0 {
		if firstErr != nil {
			return nil, nil, nil, firstErr
		}
		return nil, nil, nil, fmt.Errorf("no CA bundle found among %s", strings.Join(linuxCABundles, ", "))
	}
	return fps, sources, skipped, nil
}

// pemFingerprints returns the lowercase hex SHA-256 of the DER of every
// CERTIFICATE block in data. Blocks of other types are skipped, notably
// OpenSSL "TRUSTED CERTIFICATE" blocks (which carry trust attributes after the
// DER and so cannot be hashed this way); distribution bundles use plain
// CERTIFICATE blocks. Parsing stops at the first malformed block, keeping the
// fingerprints found before it.
func pemFingerprints(data []byte) []string {
	var out []string
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return out
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		sum := sha256.Sum256(block.Bytes)
		out = append(out, hex.EncodeToString(sum[:]))
	}
}

var securityHashLine = regexp.MustCompile(`(?m)^SHA-256 hash:\s*([0-9A-Fa-f]{64})\s*$`)

// parseSecurityFingerprints extracts SHA-256 fingerprints from the output of
// `security find-certificate -a -Z`.
func parseSecurityFingerprints(out []byte) []string {
	var fps []string
	for _, m := range securityHashLine.FindAllSubmatch(out, -1) {
		fps = append(fps, strings.ToLower(string(m[1])))
	}
	return fps
}

func darwinTrustFingerprints(ctx context.Context, run proc.Runner) (map[string]bool, []string, error) {
	queries := []struct {
		label string
		args  []string
	}{
		{darwinSystemRoots, []string{"find-certificate", "-a", "-Z", darwinSystemRoots}},
		{"default keychain search list", []string{"find-certificate", "-a", "-Z"}},
	}
	fps := map[string]bool{}
	var sources []string
	for _, q := range queries {
		res, err := run.Run(ctx, proc.Cmd{Name: "security", Args: q.args})
		if err != nil {
			return nil, nil, fmt.Errorf("security %s: %w", strings.Join(q.args, " "), err)
		}
		found := parseSecurityFingerprints(res.Stdout)
		if res.ExitCode != 0 && len(found) == 0 {
			continue
		}
		sources = append(sources, q.label)
		for _, fp := range found {
			fps[fp] = true
		}
	}
	if len(sources) == 0 {
		return nil, nil, errors.New("security find-certificate returned no certificates")
	}
	return fps, sources, nil
}

// trustStoreFindings turns a result into report findings. The wording states
// presence only; it never claims the host is compromised. Remediation is
// OS-specific: updating the CA bundle works on Linux, while on macOS the
// vendor's trust status for the root decides what the user can do.
func trustStoreFindings(res TrustStoreResult) []Finding {
	var out []Finding
	for _, e := range res.Present {
		f := Finding{
			Severity: "high",
			Title:    fmt.Sprintf("the host trust store still contains %s", e.Name),
			Detail: fmt.Sprintf("%s (SHA-256 %s) is affected by a CA key compromise (as of %s, source %s). "+
				"Update the OS/browser trust store. This does not mean the host is compromised.",
				e.Name, e.CertSHA256, e.AsOf, e.Source),
		}
		// The vendor-status sentence below (Apple included, Mozilla removed,
		// Chrome blocked) is hardcoded for every root and is only true for
		// the single root currently in the table. Before adding another root,
		// add per-entry vendor-status data or revise this wording
		// (TestShippedTableRootsHaveKnownVendorStatus enforces this).
		if res.OS == hostos.Darwin {
			f.Severity = "medium"
			f.Detail = fmt.Sprintf("%s (SHA-256 %s) is affected by a CA key compromise (source %s). "+
				"At the time this table was last updated (as of %s), Apple still listed this root as included while Mozilla had removed it and Chrome had blocked it, "+
				"so macOS may still trust it. Check for a macOS update, or adjust the certificate's trust settings in Keychain Access. "+
				"This does not mean the host is compromised.",
				e.Name, e.CertSHA256, e.Source, e.AsOf)
		}
		out = append(out, f)
	}
	return out
}

// hostTrustFindings runs check (default CheckTrustStore) and converts the
// outcome to findings. A host OS without a backend is silently skipped; any
// other failure is reported as info so a skipped check is visible.
func hostTrustFindings(ctx context.Context, check func(context.Context) (TrustStoreResult, error)) []Finding {
	if check == nil {
		check = func(ctx context.Context) (TrustStoreResult, error) {
			ctx, cancel := context.WithTimeout(ctx, trustStoreTimeout)
			defer cancel()
			return CheckTrustStore(ctx, TrustStoreOptions{})
		}
	}
	res, err := check(ctx)
	if errors.Is(err, hostos.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return []Finding{{Severity: "info", Title: "host trust store check skipped", Detail: err.Error()}}
	}
	return trustStoreFindings(res)
}
