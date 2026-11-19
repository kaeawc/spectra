package process

import (
	"context"

	"github.com/kaeawc/spectra/internal/classify"
)

// Classify fills Role, Runtime, App, Signing and Evidence on procs in place.
// It returns ctx.Err() when cancelled; procs are still fully populated, with
// runtime unknown for executables the cold phase did not reach.
func Classify(ctx context.Context, procs []Info, c *classify.Classifier) error {
	inputs := make([]classify.Input, len(procs))
	for i, p := range procs {
		inputs[i] = classify.Input{
			PID:            p.PID,
			PPID:           p.PPID,
			UID:            p.UID,
			Command:        p.Command,
			ExecutablePath: p.ExecutablePath,
			CommandLine:    p.FullCommandLine,
		}
	}
	results, err := c.Classify(ctx, inputs)
	for i, r := range results {
		procs[i].Role = r.Role
		procs[i].Runtime = r.Runtime
		procs[i].App = r.App
		procs[i].Signing = r.Signing
		procs[i].Evidence = r.Evidence
	}
	return err
}
