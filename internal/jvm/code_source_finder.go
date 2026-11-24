package jvm

import (
	"context"

	"github.com/kaeawc/spectra/internal/process"
)

// CodeSourceFinder reads a JVM's launch class path from its argv and cwd
// (kernel reads; the JVM is not contacted) and resolves frame classes.
type CodeSourceFinder struct {
	Argv     func(pid int) ([]string, error)
	Cwd      func(ctx context.Context, pid int) (string, error)
	Resolver *CodeSourceResolver
}

// DefaultCodeSourceFinder reads the real host. Its resolver caches jar
// indexes, so a long-lived holder (the daemon) reuses them across calls.
func DefaultCodeSourceFinder() CodeSourceFinder {
	return CodeSourceFinder{Argv: process.Argv, Cwd: process.CwdReader{}.Cwd, Resolver: NewCodeSourceResolver()}
}

// Find maps dump's frame classes to their jar or class file; nil when the
// launch class path cannot be read.
func (f CodeSourceFinder) Find(ctx context.Context, pid int, dump ParsedThreadDump) map[string]CodeSource {
	argv, err := f.Argv(pid)
	if err != nil || len(argv) == 0 {
		return nil
	}
	cp := ParseLaunchArgs(argv)
	if cp.Unknown {
		return nil
	}
	var cwd string
	if cp.NeedsCwd() {
		if cwd, err = f.Cwd(ctx, pid); err != nil {
			cwd = ""
		}
	}
	return f.Resolver.CodeSourcesForDump(ctx, dump, cp, cwd)
}
