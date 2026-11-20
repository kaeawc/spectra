package classify

import (
	"context"
	"fmt"
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
	// certificate named "Software Signing", confirmed by
	// `codesign --verify --ignore-resources -R="anchor apple"`.
	SigningApple SigningStatus = "apple"
	// SigningDeveloperID is third-party code signed with an Apple-issued
	// certificate that carries a team ID: Developer ID, Mac App Store, or
	// Apple Development/Distribution. The team ID is what `codesign -dv`
	// displays; it is not validated.
	SigningDeveloperID SigningStatus = "developer_id"
	SigningAdhoc       SigningStatus = "adhoc"
	SigningUnsigned    SigningStatus = "unsigned"
	// SigningInvalid is code whose `codesign -dv` output claims Apple (a
	// platform identifier or a "Software Signing" leaf) but which fails
	// `codesign --verify --ignore-resources -R="anchor apple"`: a damaged or forged signature.
	SigningInvalid SigningStatus = "invalid"
	SigningUnknown SigningStatus = "unknown"
)

// Signing is the code-signing summary of one executable (macOS only).
type Signing struct {
	Status          SigningStatus `json:"status"`
	TeamID          string        `json:"team_id,omitempty"`
	HardenedRuntime bool          `json:"hardened_runtime"`
}

// signingVerdict is Signing plus what role and the evidence line need.
type signingVerdict struct {
	Signing Signing `json:"signing"`
	// Platform is set only for a validated Apple platform binary.
	Platform bool `json:"platform,omitempty"`
	// Claim is the Apple claim found in the display output ("platform
	// binary" or "Software Signing leaf"); empty when there was none.
	Claim string `json:"claim,omitempty"`
	// Validated is set when the Apple claim passed codesign --verify.
	Validated bool `json:"validated,omitempty"`
}

const codesignTimeout = 10 * time.Second

// appleRequirement is the code requirement only Apple's own signatures meet.
const appleRequirement = "anchor apple"

// readSigning runs codesign for exe: once to display the signature and,
// only when that display claims Apple, again to verify the claim. It never
// shells out off Darwin.
func readSigning(ctx context.Context, osKind hostos.Kind, runner proc.Runner, exe string) (signingVerdict, error) {
	if hostos.Resolve(osKind) != hostos.Darwin {
		return signingVerdict{}, hostos.Unsupported("codesign inspection")
	}
	ctx, cancel := context.WithTimeout(ctx, codesignTimeout)
	defer cancel()
	res, err := runner.Run(ctx, proc.Cmd{Name: "codesign", Args: []string{"-dv", "--verbose=2", exe}})
	if err != nil {
		return signingVerdict{Signing: Signing{Status: SigningUnknown}}, fmt.Errorf("codesign -dv: %w", err)
	}
	v := parseCodesign(string(res.Stderr) + string(res.Stdout))
	if v.Claim == "" {
		return v, nil
	}
	// --ignore-resources: the question is whether the executable's code and
	// signature are Apple's. Code pages and the CodeDirectory are still
	// checked; resource seals are not, because genuine Apple bundles
	// (Simulator runtimes, Safari's cryptex XPC services, XProtect) fail
	// them with "resource envelope is obsolete".
	res, err = runner.Run(ctx, proc.Cmd{Name: "codesign", Args: []string{"--verify", "--ignore-resources", "-R=" + appleRequirement, exe}})
	if err != nil {
		return signingVerdict{Signing: Signing{Status: SigningUnknown}, Claim: v.Claim}, fmt.Errorf("codesign --verify: %w", err)
	}
	return applyVerify(v, res.ExitCode == 0), nil
}

// applyVerify settles a display-output Apple claim with the verify result.
func applyVerify(v signingVerdict, ok bool) signingVerdict {
	if ok {
		v.Validated = true
		v.Platform = v.Claim == claimPlatform
		v.Signing.Status = SigningApple
		return v
	}
	v.Signing.Status = SigningInvalid
	return v
}

// parseCodesign classifies `codesign -dv --verbose=2` output. That output
// displays claims without validating them, so an Apple claim only sets
// Claim (status unknown, Platform false) for applyVerify to settle.
func parseCodesign(out string) signingVerdict {
	var v signingVerdict
	v.Signing.TeamID, v.Signing.HardenedRuntime = detect.ParseSigningOutput(out)
	if strings.Contains(out, "is not signed at all") {
		v.Signing = Signing{Status: SigningUnsigned}
		return v
	}
	var leaf string
	adhoc, platform := false, false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Platform identifier="):
			platform = true
		case strings.HasPrefix(line, "Signature=adhoc"):
			adhoc = true
		case strings.HasPrefix(line, "Authority=") && leaf == "":
			leaf = strings.TrimPrefix(line, "Authority=")
		}
	}
	switch {
	case platform:
		v.Claim = claimPlatform
	case leaf == "Software Signing" || strings.HasSuffix(leaf, " Software Signing"):
		v.Claim = "Software Signing leaf"
	}
	v.Signing.Status = signingStatus(leaf, adhoc, v.Claim != "")
	return v
}

const claimPlatform = "platform binary"

func signingStatus(leaf string, adhoc, appleClaim bool) SigningStatus {
	switch {
	case appleClaim:
		return SigningUnknown
	case adhoc:
		return SigningAdhoc
	case thirdPartyAppleCert(leaf):
		return SigningDeveloperID
	}
	return SigningUnknown
}

// signingEvidence is the evidence line for a signing verdict. The verdict
// always describes the file on disk, which may no longer be the code a
// long-running process loaded.
func signingEvidence(v signingVerdict) string {
	const disk = "verdict is for the file on disk"
	verify := fmt.Sprintf("codesign --verify --ignore-resources -R=%q", appleRequirement)
	switch {
	case v.Validated:
		return fmt.Sprintf("signing: %s validated by %s; %s", v.Claim, verify, disk)
	case v.Signing.Status == SigningInvalid:
		return fmt.Sprintf("signing: codesign -dv claims %s but %s failed; %s", v.Claim, verify, disk)
	case v.Claim != "":
		return fmt.Sprintf("signing: %s claim could not be checked; %s", v.Claim, disk)
	case v.Signing.TeamID != "":
		return fmt.Sprintf("signing: %s, team %s as displayed by codesign -dv, not validated; %s", v.Signing.Status, v.Signing.TeamID, disk)
	}
	return fmt.Sprintf("signing: %s as displayed by codesign -dv, not validated; %s", v.Signing.Status, disk)
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
