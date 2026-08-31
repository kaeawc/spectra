package dbinspect

import (
	"os/exec"
	"strconv"
	"strings"
)

// CmdRunner runs an external command and returns combined stdout. Injected
// so tests can replay lsof fixtures.
type CmdRunner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// FileCandidate is one SQLite database file a running process holds open.
type FileCandidate struct {
	Engine  Engine `json:"engine"`
	PID     int    `json:"pid,omitempty"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path"`
}

// sqliteSidecarSuffixes are trimmed before matching so a process holding
// only the WAL or SHM still points at its database file.
var sqliteSidecarSuffixes = []string{"-wal", "-shm", "-journal"}

// DiscoverSQLiteFiles scans open regular files for SQLite database paths.
// One host-wide `lsof -nP` call, the same primitive the connections
// collector uses; sidecar (-wal/-shm/-journal) handles are folded into
// their database path, and duplicate pid+path pairs collapse.
func DiscoverSQLiteFiles(run CmdRunner) []FileCandidate {
	out, err := run("lsof", "-nP")
	if err != nil {
		return nil
	}
	return parseLSOFSQLiteFiles(string(out))
}

// parseLSOFSQLiteFiles extracts SQLite file candidates from `lsof -nP`
// output. Column order: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME;
// NAME may contain spaces, so it is the join of the trailing fields.
func parseLSOFSQLiteFiles(out string) []FileCandidate {
	var candidates []FileCandidate
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[4] != "REG" {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		path := sqliteDatabasePath(strings.Join(fields[8:], " "))
		if path == "" {
			continue
		}
		key := fields[1] + "\x00" + path
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, FileCandidate{
			Engine:  EngineSQLite,
			PID:     pid,
			Command: fields[0],
			Path:    path,
		})
	}
	return candidates
}

// sqliteDatabasePath returns the database path an open file points at, or
// "" when the file doesn't look like an SQLite database.
func sqliteDatabasePath(name string) string {
	for _, sidecar := range sqliteSidecarSuffixes {
		name = strings.TrimSuffix(name, sidecar)
	}
	for _, suffix := range sqliteFileSuffixes {
		if strings.HasSuffix(name, suffix) {
			return name
		}
	}
	return ""
}
