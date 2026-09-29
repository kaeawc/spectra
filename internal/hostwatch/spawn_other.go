//go:build !darwin && !linux

package hostwatch

func newSpawnBackend() SpawnBackend { return nil }
