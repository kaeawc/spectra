//go:build !darwin && !linux

package hostwatch

import (
	"context"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func notifyDesktop(context.Context, hostos.Kind, proc.Runner, func(string) (string, error), Alert) error {
	return nil
}
