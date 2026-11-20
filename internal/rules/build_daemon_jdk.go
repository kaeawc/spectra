package rules

import (
	"fmt"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// ruleBuildDaemonJDKGone fires for a running Gradle or Kotlin daemon whose
// java.home or executable no longer exists. Builds reconnect to such a daemon
// and fail starting JDK tools, with nothing on the host naming the cause.
func ruleBuildDaemonJDKGone() Rule {
	return Rule{
		ID:       "build-daemon-jdk-gone",
		Severity: SeverityHigh,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			var findings []Finding
			for _, j := range s.JVMs {
				kind := jvm.BuildDaemonKind(j.MainClass)
				if kind == "" || (!j.JavaHomeMissing && !j.ExecutableDeleted) {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   "build-daemon-jdk-gone",
					Severity: SeverityHigh,
					Subject:  fmt.Sprintf("PID %d (%s)", j.PID, j.MainClass),
					Message: fmt.Sprintf("%s runs on a JDK that no longer exists on disk (java.home=%q); builds reusing it can fail starting JDK tools such as jlink.",
						kind, j.JavaHome),
					Fix: fmt.Sprintf("Let it idle out or stop just this daemon with `kill %d`. Do not run `gradle --stop`: it stops every compatible daemon on the host. See `spectra jvm explain %d`.", j.PID, j.PID),
				})
			}
			return findings
		},
	}
}
