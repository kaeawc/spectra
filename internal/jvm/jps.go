package jvm

import (
	"os/exec"
	"strconv"
	"strings"
)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// discoverPIDs runs `jps -l` and returns a map of PID → main class.
// Returns nil if jps is not found or returns an error (no JDK in PATH).
func discoverPIDs(run CmdRunner) map[int]string {
	out, err := run("jps", "-l")
	if err != nil {
		return nil
	}
	return parseJPS(string(out))
}

// parseJPS parses `jps -l` output into a PID → main-class map.
//
// Format (one process per line):
//
//	12345 com.example.Main
//	23456 org.gradle.launcher.daemon.bootstrap.GradleDaemon
//	34567
//	56789 sun.tools.jps.Jps   ← jps itself; excluded
func parseJPS(out string) map[int]string {
	result := make(map[int]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, rest, _ := strings.Cut(line, " ")
		pidN, err := strconv.Atoi(pid)
		if err != nil {
			continue
		}
		main := strings.TrimSpace(rest)
		// Skip jps itself and the Jstat internal tools.
		if strings.HasPrefix(main, "sun.tools.") {
			continue
		}
		result[pidN] = main
	}
	return result
}
