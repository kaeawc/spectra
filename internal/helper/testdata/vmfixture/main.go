// Command vmfixture is the trivial launch daemon the helper_vm integration
// suite installs inside the disposable test VM. It waits for SIGTERM and
// exits. It lives under testdata so `go build ./...` never builds it;
// scripts/helper-vm-test.sh builds it explicitly for darwin/arm64.
package main

import (
	"os"
	"os/signal"
	"syscall"
)

func main() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	<-c
}
