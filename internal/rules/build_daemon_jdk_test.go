package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestBuildDaemonJDKGone(t *testing.T) {
	const gradle = "org.gradle.launcher.daemon.bootstrap.GradleDaemon"
	const kotlin = "org.jetbrains.kotlin.daemon.KotlinCompileDaemon"
	s := baseSnap()
	s.JVMs = []jvm.Info{
		{PID: 1, MainClass: gradle, JavaHome: "/gone/jdk", JavaHomeMissing: true},
		{PID: 2, MainClass: kotlin, JavaHome: "/gone/jdk", ExecutableDeleted: true},
		{PID: 3, MainClass: gradle, JavaHome: "/ok/jdk"},
		{PID: 4, MainClass: "svc.App", JavaHome: "/gone/jdk", JavaHomeMissing: true},
	}
	got := ruleBuildDaemonJDKGone().MatchFn(s)
	if len(got) != 2 {
		t.Fatalf("want findings for the Gradle and Kotlin daemons only, got %v", got)
	}
	if !strings.Contains(got[0].Message, "Gradle daemon") || !strings.Contains(got[1].Message, "Kotlin daemon") {
		t.Errorf("messages = %q / %q", got[0].Message, got[1].Message)
	}
	if got[0].Severity != SeverityHigh || !strings.Contains(got[0].Fix, "kill 1") || !strings.Contains(got[0].Fix, "Do not run `gradle --stop`") {
		t.Errorf("finding = %+v", got[0])
	}
}
