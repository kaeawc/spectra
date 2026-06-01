//go:build !darwin

package storagestate

func collectMounts(_ CmdRunner) ([]Mount, error) {
	return nil, nil
}
