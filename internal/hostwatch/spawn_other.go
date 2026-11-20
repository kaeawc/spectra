//go:build !darwin && !linux

package hostwatch

import "github.com/kaeawc/spectra/internal/logger"

func newSpawnBackend(_ logger.Logger) SpawnBackend { return nil }
