//go:build !darwin

package hostwatch

import (
	"context"
	"errors"

	"github.com/kaeawc/spectra/internal/proc"
)

var errDarwinUnavailable = errors.New("darwin collector unavailable")

func darwinLoad() ([3]float64, error)                          { return [3]float64{}, errDarwinUnavailable }
func darwinMemory() (string, float64, float64, error)          { return "unknown", 0, 0, errDarwinUnavailable }
func darwinLimits(int) map[string]LimitUsage                   { return nil }
func darwinThermal(context.Context, proc.Runner) (bool, error) { return false, errDarwinUnavailable }
