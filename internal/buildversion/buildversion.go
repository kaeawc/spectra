// Package buildversion resolves the version string a Spectra binary reports.
//
// Release builds stamp the tag into each command's main.version via
// -ldflags "-X main.version=vX.Y.Z". Source builds leave the "dev" default,
// and Resolve appends the VCS revision the Go toolchain embeds so a report
// from a source build still identifies the commit it came from.
package buildversion

import (
	"runtime/debug"
	"strings"
)

// Dev is the placeholder version of a build without -X main.version.
const Dev = "dev"

// IsDev reports whether v is a source-build version: "dev", "dev+<rev>" or
// "dev+<rev>.dirty". Such builds have no release identity to compare.
func IsDev(v string) bool {
	return v == Dev || strings.HasPrefix(v, Dev+"+")
}

// shortRevision is the commit-hash prefix length reported for source builds.
const shortRevision = 12

// Resolve returns stamped unchanged when it carries a real version, and
// otherwise the module version `go install module@vX.Y.Z` embeds, or "dev",
// "dev+<rev>" or "dev+<rev>.dirty" from the build info.
func Resolve(stamped string) string {
	return resolve(stamped, debug.ReadBuildInfo)
}

func resolve(stamped string, read func() (*debug.BuildInfo, bool)) string {
	if stamped != "" && stamped != Dev {
		return stamped
	}
	info, ok := read()
	if !ok || info == nil {
		return Dev
	}
	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		// `go install module@v0.1.0` records the module version and no VCS data.
		// Builds inside a checkout also get a pseudo-version there, so the
		// module version is only trusted when the VCS revision is absent.
		if mv := info.Main.Version; mv != "" && mv != "(devel)" {
			return mv
		}
		return Dev
	}
	if len(revision) > shortRevision {
		revision = revision[:shortRevision]
	}
	v := Dev + "+" + revision
	if modified {
		v += ".dirty"
	}
	return v
}
