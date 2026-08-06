package main

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/kaeawc/spectra/internal/logger"
)

func TestStripVerboseFlags(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "")
	cases := []struct {
		name        string
		in          []string
		wantRest    []string
		wantVerbose bool
	}{
		{"none", []string{"rules", "--json"}, []string{"rules", "--json"}, false},
		{"long verbose after subcommand", []string{"rules", "--verbose"}, []string{"rules"}, true},
		{"debug before subcommand", []string{"--debug", "rules"}, []string{"rules"}, true},
		{"preserves inspect -v", []string{"inspect", "-v", "/App"}, []string{"inspect", "-v", "/App"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, verbose := stripVerboseFlags(tc.in)
			if verbose != tc.wantVerbose {
				t.Fatalf("verbose = %v, want %v", verbose, tc.wantVerbose)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Fatalf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

func TestStripVerboseFlagsHonoursEnv(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "1")
	if _, verbose := stripVerboseFlags([]string{"rules"}); !verbose {
		t.Fatal("SPECTRA_DEBUG should enable verbose")
	}
}

func TestEnableVerboseTogglesDebugLevel(t *testing.T) {
	orig := cliLogger
	t.Cleanup(func() { cliLogger = orig })

	cliLogger = logger.Discard()
	if cliLogger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("discard logger unexpectedly enables debug")
	}
	enableVerbose()
	if !cliLogger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("verbose logger should enable debug level")
	}
}
