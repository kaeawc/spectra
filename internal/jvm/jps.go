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

// discoverPIDs runs `jps -l` and returns a map of PID → main class, plus
// whether jps was available (ran without error). available is false when jps
// is not on PATH or errored, which is distinct from jps running and finding no
// JVMs (empty map, available true).
func discoverPIDs(run CmdRunner) (pids map[int]string, available bool) {
	out, err := run("jps", "-l")
	if err != nil {
		return nil, false
	}
	return parseJPS(string(out)), true
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
