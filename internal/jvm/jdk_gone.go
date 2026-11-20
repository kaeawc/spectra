package jvm

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// BuildDaemonKind names the long-lived build daemon a main class belongs to
// ("Gradle daemon", "Kotlin daemon"), or "" when it is neither. These daemons
// are reused across builds, so they keep the JDK they were started on.
func BuildDaemonKind(mainClass string) string {
	switch {
	case strings.HasSuffix(mainClass, "GradleDaemon"):
		return "Gradle daemon"
	case strings.HasSuffix(mainClass, "KotlinCompileDaemon"):
		return "Kotlin daemon"
	}
	return ""
}

// detectJDKGone records whether the JVM's java.home or executable vanished
// from disk after it started (a `brew upgrade` removing the Cellar path, a
// sandbox, a deleted install). The checks are two cheap path probes.
func detectJDKGone(info *Info, opts CollectOptions) {
	exists := opts.PathExists
	if exists == nil {
		exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	if info.JavaHome != "" && !exists(filepath.Join(info.JavaHome, "bin", "java")) {
		info.JavaHomeMissing = true
	}
	readlink := opts.ReadLink
	if readlink == nil && runtime.GOOS == "linux" {
		readlink = os.Readlink
	}
	if readlink != nil {
		if target, err := readlink(fmt.Sprintf("/proc/%d/exe", info.PID)); err == nil && strings.HasSuffix(target, " (deleted)") {
			info.ExecutableDeleted = true
		}
	}
}

// explainJDKGone adds the stale-JDK observation for a build daemon whose JDK
// vanished. It is limited to the Gradle and Kotlin daemons, which builds
// reconnect to and so keep failing; other JVMs fail visibly on their own.
func explainJDKGone(e *Explanation, info *Info) {
	kind := BuildDaemonKind(info.MainClass)
	if kind == "" || (!info.JavaHomeMissing && !info.ExecutableDeleted) {
		return
	}
	evidence := fmt.Sprintf("java.home=%q has no bin/java", info.JavaHome)
	if info.ExecutableDeleted {
		evidence += fmt.Sprintf("; /proc/%d/exe is marked (deleted)", info.PID)
	}
	e.Observations = append(e.Observations, Observation{
		ID:             "build-daemon-jdk-gone",
		Severity:       "high",
		Summary:        fmt.Sprintf("This %s runs on a JDK that no longer exists on disk, so builds that reuse it can fail when they spawn JDK tools such as jlink.", kind),
		Evidence:       evidence,
		Recommendation: fmt.Sprintf("Let this daemon idle out, or stop just it with `kill %d`. Do not run `gradle --stop`: it stops every compatible daemon on the host.", info.PID),
	})
}
