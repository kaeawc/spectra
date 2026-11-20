package classify

import (
	"context"
	"errors"
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
	// Apple Development/Distribution. Signing.Validated says whether
	// `codesign --verify --strict` accepted it.
	SigningDeveloperID SigningStatus = "developer_id"
	SigningAdhoc       SigningStatus = "adhoc"
	SigningUnsigned    SigningStatus = "unsigned"
	// SigningInvalid is code whose signature fails verification: a
	// `codesign -dv` Apple claim (a platform identifier or a "Software
	// Signing" leaf) that fails `codesign --verify --ignore-resources
	// -R="anchor apple"`, or a third-party Apple-issued signature that fails
	// `codesign --verify --strict`. Either is damaged, tampered or forged.
	SigningInvalid SigningStatus = "invalid"
	SigningUnknown SigningStatus = "unknown"
)

// Signing is the code-signing summary of one executable (macOS only).
type Signing struct {
	Status          SigningStatus `json:"status"`
	TeamID          string        `json:"team_id,omitempty"`
	HardenedRuntime bool          `json:"hardened_runtime"`
	// Validated is true only when a codesign --verify step passed: the
	// Apple requirement for apple, --strict for developer_id. Every other
	// status, and a developer_id whose verify could not settle it, is what
	// `codesign -dv` displays.
	Validated bool `json:"validated"`
}

// signingVerdict is Signing plus what role and the evidence line need.
type signingVerdict struct {
	Signing Signing `json:"signing"`
	// Platform is set only for a validated Apple platform binary.
	Platform bool `json:"platform,omitempty"`
	// Claim is the Apple claim found in the display output ("platform
	// binary" or "Software Signing leaf"); empty when there was none.
	Claim string `json:"claim,omitempty"`
	// Detritus is set when codesign --verify --strict rejected only
	// extended attributes or resource forks in the bundle: a packaging
	// defect, not evidence of altered code, so the status is kept and
	// Signing.Validated stays false.
	Detritus bool `json:"detritus,omitempty"`
}

const codesignTimeout = 10 * time.Second

// appleRequirement is the code requirement only Apple's own signatures meet.
const appleRequirement = "anchor apple"

// readSigning runs codesign for exe: once to display the signature and, when
// that display claims Apple or shows a third-party Apple-issued certificate,
// again to verify it. It never shells out off Darwin.
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
	switch {
	case v.Claim != "":
		return verifyApple(ctx, runner, exe, v)
	case v.Signing.Status == SigningDeveloperID:
		return verifyThirdParty(ctx, runner, exe, v)
	}
	return v, nil
}

// verifyApple settles a display-output Apple claim.
//
// --ignore-resources: the question is whether the executable's code and
// signature are Apple's. Code pages and the CodeDirectory are still checked;
// resource seals are not, because genuine Apple bundles (Simulator runtimes,
// Safari's cryptex XPC services, XProtect) fail them with "resource envelope
// is obsolete".
func verifyApple(ctx context.Context, runner proc.Runner, exe string, v signingVerdict) (signingVerdict, error) {
	res, err := runner.Run(ctx, proc.Cmd{Name: "codesign", Args: []string{"--verify", "--ignore-resources", "-R=" + appleRequirement, exe}})
	if err == nil {
		err = noVerdict(res)
	}
	if err != nil {
		return signingVerdict{Signing: Signing{Status: SigningUnknown}, Claim: v.Claim}, fmt.Errorf("codesign --verify: %w", err)
	}
	return applyVerify(v, res.ExitCode == 0), nil
}

// verifyThirdParty checks a third-party Apple-issued signature against its
// own designated requirement. Resources are not ignored: for an Electron or
// JVM app the code that runs lives in the bundle's sealed resources. When
// the verify cannot run, the displayed status is kept unvalidated and the
// error makes the caller retry it on the next call.
func verifyThirdParty(ctx context.Context, runner proc.Runner, exe string, v signingVerdict) (signingVerdict, error) {
	res, err := runner.Run(ctx, proc.Cmd{Name: "codesign", Args: []string{"--verify", "--strict", exe}})
	if err == nil {
		err = noVerdict(res)
	}
	if err != nil {
		return v, fmt.Errorf("codesign --verify --strict: %w", err)
	}
	switch {
	case res.ExitCode == 0:
		v.Signing.Validated = true
	case onlyDetritus(string(res.Stderr) + string(res.Stdout)):
		v.Detritus = true
	default:
		v.Signing.Status = SigningInvalid
	}
	return v, nil
}

// errVanished marks a verify that failed because the file was gone (an
// update or uninstall racing the listing): that says nothing about the code.
var errVanished = errors.New("executable vanished during codesign --verify")

// noVerdict turns a verify that did not produce a verdict into an error, so
// the caller retries and never caches it. Only exit 0 (passed), 1 (invalid
// signature) and 3 (requirement not met) are verdicts. Exit 2 is a usage or
// internal error, and a codesign killed by a signal reports -1 with no error
// from the runner.
func noVerdict(res proc.Result) error {
	if res.ExitCode != 0 && strings.Contains(string(res.Stderr), "No such file or directory") {
		return errVanished
	}
	switch res.ExitCode {
	case 0, 1, 3:
		return nil
	}
	return fmt.Errorf("codesign --verify exited %d without a verdict", res.ExitCode)
}

// detritusComplaint is codesign --strict's rejection of extended attributes
// or resource forks on files in the signed code.
const detritusComplaint = "resource fork, Finder information, or similar detritus not allowed"

// onlyDetritus reports whether the detritus rejection is the only complaint
// codesign printed.
func onlyDetritus(out string) bool {
	found := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "In subcomponent:") || strings.HasPrefix(line, "In architecture:"):
		case strings.HasSuffix(line, detritusComplaint):
			found = true
		default:
			return false
		}
	}
	return found
}

// applyVerify settles a display-output Apple claim with the verify result.
func applyVerify(v signingVerdict, ok bool) signingVerdict {
	if ok {
		v.Signing.Validated = true
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

const strictVerify = "codesign --verify --strict"

// signingEvidence is the evidence line for a signing verdict. The verdict
// always describes the file on disk, which may no longer be the code a
// long-running process loaded.
func signingEvidence(v signingVerdict) string {
	const disk = "verdict is for the file on disk"
	if v.Claim != "" {
		return appleEvidence(v) + "; " + disk
	}
	shown := string(v.Signing.Status)
	if v.Signing.TeamID != "" {
		shown += ", team " + v.Signing.TeamID
	}
	switch {
	case v.Signing.Validated:
		return fmt.Sprintf("signing: %s validated by %s; %s", shown, strictVerify, disk)
	case v.Signing.Status == SigningInvalid:
		return fmt.Sprintf("signing: codesign -dv displays an Apple-issued certificate but %s failed; %s", strictVerify, disk)
	case v.Detritus:
		return fmt.Sprintf("signing: %s as displayed by codesign -dv, not validated: %s rejected extended attributes or resource forks; %s", shown, strictVerify, disk)
	case v.Signing.Status == SigningDeveloperID:
		return fmt.Sprintf("signing: %s as displayed by codesign -dv, not validated: %s could not run; %s", shown, strictVerify, disk)
	}
	return fmt.Sprintf("signing: %s as displayed by codesign -dv, not validated; %s", shown, disk)
}

func appleEvidence(v signingVerdict) string {
	verify := fmt.Sprintf("codesign --verify --ignore-resources -R=%q", appleRequirement)
	switch {
	case v.Signing.Validated:
		return fmt.Sprintf("signing: %s validated by %s", v.Claim, verify)
	case v.Signing.Status == SigningInvalid:
		return fmt.Sprintf("signing: codesign -dv claims %s but %s failed", v.Claim, verify)
	}
	return fmt.Sprintf("signing: %s claim could not be checked", v.Claim)
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
