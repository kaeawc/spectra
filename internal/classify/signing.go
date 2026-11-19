package classify

import (
	"context"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// SigningStatus is the trust class of an executable's code signature.
type SigningStatus string

const (
	// SigningApple is Apple's own code: a platform binary or a leaf
	// certificate named "Software Signing".
	SigningApple SigningStatus = "apple"
	// SigningDeveloperID is third-party code signed with an Apple-issued
	// certificate that carries a team ID: Developer ID, Mac App Store, or
	// Apple Development/Distribution.
	SigningDeveloperID SigningStatus = "developer_id"
	SigningAdhoc       SigningStatus = "adhoc"
	SigningUnsigned    SigningStatus = "unsigned"
	SigningUnknown     SigningStatus = "unknown"
)

// Signing is the code-signing summary of one executable (macOS only).
type Signing struct {
	Status          SigningStatus `json:"status"`
	TeamID          string        `json:"team_id,omitempty"`
	HardenedRuntime bool          `json:"hardened_runtime"`
}

// signingVerdict is Signing plus the platform-binary bit role uses.
type signingVerdict struct {
	Signing  Signing `json:"signing"`
	Platform bool    `json:"platform,omitempty"`
}

const codesignTimeout = 10 * time.Second

// readSigning runs codesign once for exe. It never shells out off Darwin.
func readSigning(ctx context.Context, osKind hostos.Kind, runner proc.Runner, exe string) (signingVerdict, error) {
	if hostos.Resolve(osKind) != hostos.Darwin {
		return signingVerdict{}, hostos.Unsupported("codesign inspection")
	}
	ctx, cancel := context.WithTimeout(ctx, codesignTimeout)
	defer cancel()
	res, err := runner.Run(ctx, proc.Cmd{Name: "codesign", Args: []string{"-dv", "--verbose=2", exe}})
	if err != nil {
		return signingVerdict{Signing: Signing{Status: SigningUnknown}}, err
	}
	return parseCodesign(string(res.Stderr) + string(res.Stdout)), nil
}

// parseCodesign classifies `codesign -dv --verbose=2` output.
func parseCodesign(out string) signingVerdict {
	var v signingVerdict
	v.Signing.TeamID, v.Signing.HardenedRuntime = detect.ParseSigningOutput(out)
	if strings.Contains(out, "is not signed at all") {
		v.Signing = Signing{Status: SigningUnsigned}
		return v
	}
	var leaf string
	adhoc := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Platform identifier="):
			v.Platform = true
		case strings.HasPrefix(line, "Signature=adhoc"):
			adhoc = true
		case strings.HasPrefix(line, "Authority=") && leaf == "":
			leaf = strings.TrimPrefix(line, "Authority=")
		}
	}
	v.Signing.Status = signingStatus(leaf, adhoc, v.Platform)
	return v
}

func signingStatus(leaf string, adhoc, platform bool) SigningStatus {
	switch {
	case adhoc:
		return SigningAdhoc
	case platform || leaf == "Software Signing" || strings.HasSuffix(leaf, " Software Signing"):
		return SigningApple
	case thirdPartyAppleCert(leaf):
		return SigningDeveloperID
	}
	return SigningUnknown
}

func thirdPartyAppleCert(leaf string) bool {
	for _, prefix := range []string{
		"Developer ID Application:",
		"Apple Mac OS Application Signing",
		"Apple Development:",
		"Apple Distribution:",
		"Mac Developer:",
		"3rd Party Mac Developer Application:",
	} {
		if strings.HasPrefix(leaf, prefix) {
			return true
		}
	}
	return false
}
