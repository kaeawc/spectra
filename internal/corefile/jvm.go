package corefile

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/artifact"
)

// JVMAnalyzer contributes HotSpot Serviceability Agent commands for JVM cores.
type JVMAnalyzer struct{}

func (JVMAnalyzer) Name() string { return "jvm-serviceability-agent" }

func (JVMAnalyzer) Supports(_ context.Context, a Artifact, probe []byte) bool {
	exe := filepath.Base(a.ExecutablePath)
	if exe == "java" || exe == "java.exe" {
		return true
	}
	lowerExe := strings.ToLower(a.ExecutablePath)
	if strings.Contains(lowerExe, "/jre/") || strings.Contains(lowerExe, "/jdk") {
		return true
	}
	for _, marker := range [][]byte{
		[]byte("HotSpot"),
		[]byte("OpenJDK"),
		[]byte("Java HotSpot(TM)"),
		[]byte("java.lang.Thread"),
	} {
		if bytes.Contains(probe, marker) {
			return true
		}
	}
	return false
}

func (JVMAnalyzer) Analyze(_ context.Context, a Artifact, _ []byte) (Report, error) {
	report := Report{
		Artifact: a,
		Runtime:  "jvm",
		Observations: []Observation{
			{Key: "serviceability_agent", Value: "offline JVM inspection requires a matching JDK for the crashed JVM"},
		},
	}
	if a.ExecutablePath == "" {
		report.Observations = append(report.Observations, Observation{
			Key:   "executable_path",
			Value: "missing; pass --exe so jhsdb can resolve VM symbols",
		})
		return report, nil
	}
	report.Commands = append(report.Commands,
		Command{
			Tool:        "jhsdb",
			Args:        []string{"jstack", "--exe", a.ExecutablePath, "--core", a.Path},
			Purpose:     "Extract Java and native thread state from a crashed JVM core",
			Sensitivity: artifact.SensitivityMediumHigh,
		},
		Command{
			Tool:        "jhsdb",
			Args:        []string{"jmap", "--histo", "--exe", a.ExecutablePath, "--core", a.Path},
			Purpose:     "Extract a heap class histogram from a crashed JVM core",
			Sensitivity: artifact.SensitivityVeryHigh,
		},
		Command{
			Tool:        "jhsdb",
			Args:        []string{"jmap", "--binaryheap", "--dumpfile", "<heap.hprof>", "--exe", a.ExecutablePath, "--core", a.Path},
			Purpose:     "Write an HPROF heap dump from a crashed JVM core",
			Sensitivity: artifact.SensitivityVeryHigh,
		},
	)
	return report, nil
}
