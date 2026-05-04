package storagestate

import "os/exec"

func execRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}
