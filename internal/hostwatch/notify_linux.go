package hostwatch

import (
	"context"
	"errors"
	"os/exec"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func notifyDesktop(ctx context.Context, os hostos.Kind, run proc.Runner, look func(string) (string, error), a Alert) error {
	if os != hostos.Linux {
		return nil
	}
	if look == nil {
		look = exec.LookPath
	}
	path, err := look("notify-send")
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = proc.Output(ctx, run, path, "Spectra: "+a.Title, a.Detail)
	return err
}
