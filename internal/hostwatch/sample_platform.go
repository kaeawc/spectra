package hostwatch

import (
	"context"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func platformLoad(os hostos.Kind, read func(string) ([]byte, error)) ([3]float64, error) {
	if os == hostos.Darwin {
		return darwinLoad()
	}
	return linuxLoad(read)
}
func platformMemory(os hostos.Kind, read func(string) ([]byte, error)) (string, float64, float64, error) {
	if os == hostos.Darwin {
		return darwinMemory()
	}
	return linuxMemory(read)
}
func platformLimits(os hostos.Kind, read func(string) ([]byte, error), n, uidCount int) map[string]LimitUsage {
	if os == hostos.Darwin {
		return darwinLimits(n, uidCount)
	}
	return linuxLimits(read, n, uidCount)
}
func platformThermal(ctx context.Context, os hostos.Kind, run proc.Runner) (bool, error) {
	if os == hostos.Darwin {
		return darwinThermal(ctx, run)
	}
	return false, nil
}
