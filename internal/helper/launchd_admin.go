package helper

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/proc"
)

// dsmemberAdmin asks Directory Services whether uid is in admin. It takes
// the numeric uid, so it works without cgo user lookups, and it fails
// closed on any output it does not recognise.
func dsmemberAdmin(r proc.Runner) func(ctx context.Context, uid uint32) (bool, error) {
	return func(ctx context.Context, uid uint32) (bool, error) {
		res, err := r.Run(ctx, proc.Cmd{Name: dsmemberutilBin, Args: []string{"checkmembership", "-u", strconv.FormatUint(uint64(uid), 10), "-G", "admin"}, Env: cmdEnv})
		if err != nil {
			return false, fmt.Errorf("dsmemberutil: %w", err)
		}
		if res.ExitCode != 0 {
			return false, fmt.Errorf("dsmemberutil exited %d: %s", res.ExitCode, joinOutput(res))
		}
		switch strings.TrimSpace(string(res.Stdout)) {
		case "user is a member of the group":
			return true, nil
		case "user is not a member of the group":
			return false, nil
		}
		return false, fmt.Errorf("dsmemberutil: unexpected output %q", strings.TrimSpace(string(res.Stdout)))
	}
}
