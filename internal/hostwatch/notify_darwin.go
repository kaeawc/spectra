package hostwatch

import (
	"context"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func notifyDesktop(ctx context.Context, os hostos.Kind, run proc.Runner, _ func(string) (string, error), a Alert) error {
	if os != hostos.Darwin {
		return nil
	}
	_, err := proc.Output(ctx, run, "osascript", "-e", `display notification "`+appleEscape(a.Detail)+`" with title "Spectra: `+appleEscape(a.Title)+`"`)
	return err
}
