package logquery

import (
	"context"
	"regexp"
)

// Report runs q and keeps the entries grep matches (all when grep is nil).
// It is what `spectra logs --json` prints and what the daemon's logs.query
// returns, so both build it here.
func Report(ctx context.Context, q Query, grep *regexp.Regexp) (Result, error) {
	result, err := Run(ctx, q)
	if err != nil {
		return result, err
	}
	if grep != nil {
		result.Entries = Filter(result.Entries, grep)
	}
	return result, nil
}

// Filter keeps the entries whose message or format string matches re.
func Filter(entries []LogEntry, re *regexp.Regexp) []LogEntry {
	out := make([]LogEntry, 0, len(entries))
	for _, entry := range entries {
		if re.MatchString(entry.EventMessage) || re.MatchString(entry.FormatString) {
			out = append(out, entry)
		}
	}
	return out
}
