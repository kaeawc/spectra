package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/kaeawc/spectra/internal/buildversion"
	"github.com/kaeawc/spectra/internal/mcp"
)

// version is stamped by release builds with -ldflags "-X main.version=vX.Y.Z";
// source builds keep "dev", which init extends with the VCS revision.
var version = "dev"

func init() { version = buildversion.Resolve(version) }

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
	server := mcp.NewServer(bufio.NewReader(os.Stdin), os.Stdout)
	server.Version = version
	server.Verbose = *verboseFlag
	server.Run()
}
