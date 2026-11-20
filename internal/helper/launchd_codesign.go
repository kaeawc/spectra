package helper

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaeawc/spectra/internal/codesignout"
	"github.com/kaeawc/spectra/internal/detect"
)

// checkSigning is check 7: the bundle verifies, carries a real team id, the
// bundle and program both satisfy that team's requirement, and neither is
// Apple-signed.
func (s *launchdService) checkSigning(ctx context.Context, v *verified) error {
	bundle, program := v.target.BundlePath, v.target.Program
	ok, err := s.verifyCode(ctx, bundle)
	if err != nil {
		return err
	}
	if !ok {
		return refuse(ReasonUnsigned, "%s does not pass codesign --verify --strict", bundle)
	}
	team, err := s.readTeam(ctx, bundle)
	if err != nil {
		return err
	}
	req := fmt.Sprintf(`-R=anchor apple generic and certificate leaf[subject.OU] = "%s"`, team)
	for _, p := range []string{bundle, program} {
		ok, err := s.verifyCode(ctx, p, req)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(ReasonTeamMismatch, "%s is not signed by team %s, the team of %s", p, team, bundle)
		}
	}
	for _, p := range []string{bundle, program} {
		if err := s.refuseApple(ctx, p); err != nil {
			return err
		}
	}
	v.target.TeamID = team
	return nil
}

// verifyCode runs codesign --verify --strict [extra] path. A --strict
// rejection whose only complaint is Finder metadata (detritus) is retried
// without --strict, which still checks code and resources (the
// internal/codesignout rule the uninstaller uses).
func (s *launchdService) verifyCode(ctx context.Context, path string, extra ...string) (bool, error) {
	args := append(append([]string{"--verify", "--strict"}, extra...), path)
	res, err := s.runCmd(ctx, codesignBin, args...)
	if err != nil {
		return false, fmt.Errorf("codesign --verify %s: %w", path, err)
	}
	if res.ExitCode == 0 {
		return true, nil
	}
	if !codesignout.OnlyDetritus(joinOutput(res)) {
		return false, nil
	}
	args = append(append([]string{"--verify"}, extra...), path)
	res, err = s.runCmd(ctx, codesignBin, args...)
	if err != nil {
		return false, fmt.Errorf("codesign --verify %s: %w", path, err)
	}
	return res.ExitCode == 0, nil
}

// readTeam reads the team id with codesign -dv, only after the signature
// verified (the uninstall.readTeamID rule). Ad hoc or missing ids refuse.
func (s *launchdService) readTeam(ctx context.Context, bundle string) (string, error) {
	res, err := s.runCmd(ctx, codesignBin, "-dv", bundle)
	if err != nil {
		return "", fmt.Errorf("codesign -dv %s: %w", bundle, err)
	}
	out := joinOutput(res)
	team, _ := detect.ParseSigningOutput(out)
	switch {
	case res.ExitCode != 0:
		return "", refuse(ReasonUnsigned, "codesign -dv %s exited %d", bundle, res.ExitCode)
	case strings.Contains(out, "Signature=adhoc"):
		return "", refuse(ReasonUnsigned, "%s is ad hoc signed and has no team id", bundle)
	case !validTeamID(team):
		return "", refuse(ReasonUnsigned, "%s has no verified team id", bundle)
	}
	return team, nil
}

// refuseApple requires `codesign --verify -R=anchor apple` to fail with
// exit 3 (requirement not met). Exit 0 is Apple-signed; anything else is no
// verdict, and the helper does not guess.
func (s *launchdService) refuseApple(ctx context.Context, path string) error {
	res, err := s.runCmd(ctx, codesignBin, "--verify", "-R=anchor apple", path)
	if err != nil {
		return fmt.Errorf("codesign --verify -R=anchor apple %s: %w", path, err)
	}
	switch res.ExitCode {
	case 0:
		return refuse(ReasonAppleSigned, "%s is signed by Apple; Apple code is never changed through the helper", path)
	case 3:
		return nil
	}
	return refuse(ReasonUnsigned, "codesign could not tell whether %s is Apple-signed (exit %d)", path, res.ExitCode)
}

func validTeamID(s string) bool {
	if len(s) != 10 {
		return false
	}
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
