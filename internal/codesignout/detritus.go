// Package codesignout interprets codesign's diagnostic output.
package codesignout

import "strings"

// DetritusComplaint is codesign --strict's rejection of extended attributes
// or resource forks on files in the signed code.
const DetritusComplaint = "resource fork, Finder information, or similar detritus not allowed"

// FinderMetadataEvidence is the evidence line for a signature that --strict
// rejected only for Finder metadata and a plain verify accepted.
const FinderMetadataEvidence = "has Finder metadata (codesign --strict rejects it); code signature verified without --strict"

// OnlyDetritus reports whether the detritus rejection is the only complaint
// codesign printed, including in nested "In subcomponent:" sections.
func OnlyDetritus(out string) bool {
	found := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "In subcomponent:") || strings.HasPrefix(line, "In architecture:"):
		case strings.HasSuffix(line, DetritusComplaint):
			found = true
		default:
			return false
		}
	}
	return found
}
