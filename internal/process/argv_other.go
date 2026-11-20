//go:build !darwin

package process

import "errors"

func sysctlProcArgs(int) ([]byte, error) {
	return nil, errors.New("kern.procargs2 is macOS only")
}
