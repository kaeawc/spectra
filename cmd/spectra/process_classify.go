package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
)

// classifyProcesses fills the --classify fields, reusing the on-disk
// classify cache when the cache root is available.
func classifyProcesses(procs []process.Info) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	opts := classify.Options{}
	if cacheStores != nil && cacheStores.Classify != nil {
		opts.Store = cacheStores.Classify
	}
	if err := process.Classify(ctx, procs, classify.New(opts)); err != nil {
		fmt.Fprintf(os.Stderr, "classify: %v (remaining processes left unknown)\n", err)
	}
}

// classAppWidth bounds the APP column; longer app names are truncated.
const classAppWidth = 16

// processClassColumns renders the extra table columns --classify adds before
// COMMAND; it is empty when classification is off. SIGN is macOS only.
type processClassColumns struct {
	on   bool
	sign bool
}

func newProcessClassColumns(on bool) processClassColumns {
	return processClassColumns{on: on, sign: on && hostos.Current() == hostos.Darwin}
}

func (c processClassColumns) header() string {
	return c.cells("ROLE", "RUNTIME", "SIGN", "APP")
}

func (c processClassColumns) row(p process.Info) string {
	sign := "-"
	if p.Signing != nil {
		sign = string(p.Signing.Status)
	}
	app := "-"
	if p.App != nil {
		app = dashIfEmpty(truncate(p.App.Name, classAppWidth))
	}
	return c.cells(dashIfEmpty(string(p.Role)), dashIfEmpty(string(p.Runtime)), sign, app)
}

func (c processClassColumns) cells(role, runtime, sign, app string) string {
	if !c.on {
		return ""
	}
	out := fmt.Sprintf("%-10s  %-8s  ", role, runtime)
	if c.sign {
		out += fmt.Sprintf("%-12s  ", sign)
	}
	return out + fmt.Sprintf("%-*s  ", classAppWidth, app)
}

func (c processClassColumns) width() int {
	switch {
	case c.sign:
		return 10 + 2 + 8 + 2 + 12 + 2 + classAppWidth + 2
	case c.on:
		return 10 + 2 + 8 + 2 + classAppWidth + 2
	}
	return 0
}
