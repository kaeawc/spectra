//go:build !darwin || !cgo

package process

func collectProcessDetails(_ []Info) map[int]Details {
	return nil
}
