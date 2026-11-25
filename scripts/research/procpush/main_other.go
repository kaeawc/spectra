//go:build !darwin

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "procpush measures macOS primitives and only runs on darwin")
	os.Exit(2)
}
