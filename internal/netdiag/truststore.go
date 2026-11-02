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
	// Present lists affected roots found in the store.
	Present []AffectedCA `json:"present,omitempty"`
}

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
		err     error
	)
	switch k := hostos.Resolve(opts.OS); k {
	case hostos.Linux:
		fps, sources, err = linuxTrustFingerprints(opts.ReadFile)
	case hostos.Darwin:
		fps, sources, err = darwinTrustFingerprints(ctx, opts.Runner)
	default:
		return TrustStoreResult{}, hostos.Unsupported("trust store check")
	}
	if err != nil {
		return TrustStoreResult{}, err
	}
	return TrustStoreResult{Sources: sources, Present: affectedRootsIn(opts.Table, fps)}, nil
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

func linuxTrustFingerprints(readFile func(string) ([]byte, error)) (map[string]bool, []string, error) {
	fps := map[string]bool{}
	var sources []string
	for _, path := range linuxCABundles {
		data, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read CA bundle %s: %w", path, err)
		}
		sources = append(sources, path)
		for _, fp := range pemFingerprints(data) {
			fps[fp] = true
		}
	}
	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("no CA bundle found among %s", strings.Join(linuxCABundles, ", "))
	}
	return fps, sources, nil
}

// pemFingerprints returns the lowercase hex SHA-256 of the DER of every
// CERTIFICATE block in data. Malformed blocks are skipped.
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
// presence only; it never claims the host is compromised.
func trustStoreFindings(res TrustStoreResult) []Finding {
	var out []Finding
	for _, e := range res.Present {
		out = append(out, Finding{
			Severity: "high",
			Title:    fmt.Sprintf("the host trust store still contains %s", e.Name),
			Detail: fmt.Sprintf("%s (SHA-256 %s) is affected by a CA key compromise (as of %s, source %s). "+
				"Update the OS/browser trust store. This does not mean the host is compromised.",
				e.Name, e.CertSHA256, e.AsOf, e.Source),
		})
	}
	return out
}

// hostTrustFindings runs check (default CheckTrustStore) and converts the
// outcome to findings. A host OS without a backend is silently skipped; any
// other failure is reported as info so a skipped check is visible.
func hostTrustFindings(ctx context.Context, check func(context.Context) (TrustStoreResult, error)) []Finding {
	if check == nil {
		check = func(ctx context.Context) (TrustStoreResult, error) {
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
