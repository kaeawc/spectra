package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kaeawc/spectra/internal/mcp"
)

var version = "dev"

func main() {
	versionFlag := flag.Bool("version", false, "Print version")
	verboseFlag := flag.Bool("verbose", false, "Enable lifecycle logging to stderr")
	flag.BoolVar(verboseFlag, "v", false, "Alias for --verbose")
	flag.Parse()

	if *versionFlag {
		fmt.Println("spectra-mcp", version)
		os.Exit(0)
	}

	log.SetOutput(os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := mcp.NewServer(os.Stdin, os.Stdout)
	server.Version = version
	server.Verbose = *verboseFlag
	if err := server.Run(ctx); err != nil {
		log.Printf("spectra-mcp: %v", err)
		stop()
		os.Exit(1)
	}
}
