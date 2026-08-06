package main

import (
	"log/slog"
	"os"

	"github.com/kaeawc/spectra/internal/logger"
)

// cliLogger is the CLI's debug logger. By default it discards everything, so
// one-shot commands stay quiet. When --verbose / --debug (or SPECTRA_DEBUG) is
// set it writes debug records to stderr, making otherwise-silent enhancement
// failures (history attach, cache writes, degraded collectors) observable.
var cliLogger logger.Logger = logger.Discard()

// enableVerbose routes cliLogger to stderr at debug level.
func enableVerbose() {
	cliLogger = logger.New(logger.Config{
		Writer: os.Stderr,
		Format: logger.FormatText,
		Level:  slog.LevelDebug,
	})
}

// stripVerboseFlags removes the global --verbose/--debug flags from args (they
// may appear before or after the subcommand) and reports whether verbose output
// was requested, also honouring the SPECTRA_DEBUG environment variable. The
// short -v is intentionally left alone: `spectra inspect -v` already means
// "show detection signals".
func stripVerboseFlags(args []string) (rest []string, verbose bool) {
	verbose = os.Getenv("SPECTRA_DEBUG") != ""
	rest = make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--verbose", "-verbose", "--debug", "-debug":
			verbose = true
		default:
			rest = append(rest, a)
		}
	}
	return rest, verbose
}
