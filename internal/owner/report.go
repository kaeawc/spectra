package owner

import (
	"context"
	"path/filepath"
	"time"
)

// DefaultTimeout bounds a whole owner lookup (the CLI's --timeout default).
const DefaultTimeout = 30 * time.Second

// FindFunc resolves a target; Finder.Find is the real one.
type FindFunc func(ctx context.Context, t Target) (Result, error)

// UnderFunc lists processes working under directories; Finder.Under is the
// real one.
type UnderFunc func(ctx context.Context, dirs []string) (UnderResult, error)

// BuildReport answers "who has this?" as the `spectra owner --json`
// document: Holders is never null.
func BuildReport(ctx context.Context, t Target, find FindFunc) (Result, error) {
	res, err := find(ctx, t)
	if err != nil {
		return Result{}, err
	}
	if res.Holders == nil {
		res.Holders = []Holder{}
	}
	return res, nil
}

// AbsDirs joins relative dirs onto cwd and cleans every one.
func AbsDirs(args []string, cwd string) []string {
	dirs := make([]string, len(args))
	for i, a := range args {
		if !filepath.IsAbs(a) {
			a = filepath.Join(cwd, a)
		}
		dirs[i] = filepath.Clean(a)
	}
	return dirs
}

// BuildUnderReport lists processes working under dirs as the
// `spectra owner --under --json` document: Holders is never null and Dirs
// falls back to dirs.
func BuildUnderReport(ctx context.Context, dirs []string, under UnderFunc) (UnderResult, error) {
	res, err := under(ctx, dirs)
	if err != nil {
		return UnderResult{}, err
	}
	if res.Holders == nil {
		res.Holders = []Holder{}
	}
	if res.Dirs == nil {
		res.Dirs = dirs
	}
	return res, nil
}
