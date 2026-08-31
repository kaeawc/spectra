package dbinspect

import (
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/netstate"
)

// Candidate is one live socket that looks like an application talking to a
// database server, inferred from the remote port.
type Candidate struct {
	Engine     Engine `json:"engine"`
	PID        int    `json:"pid,omitempty"`
	Command    string `json:"command,omitempty"`
	RemoteAddr string `json:"remote_addr"`
	Port       int    `json:"port"`
	State      string `json:"state,omitempty"`
}

// EnvHint is one environment variable that carries database connection
// details. Values are redacted before they reach reports or logs.
type EnvHint struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Engine Engine `json:"engine,omitempty"`
}

// Discovery aggregates every clue that the host (or an app on it) uses a
// database: live sockets to known server ports, connection env vars, and
// SQLite database files held open by running processes.
type Discovery struct {
	Connections []Candidate     `json:"connections,omitempty"`
	Env         []EnvHint       `json:"env,omitempty"`
	SQLiteFiles []FileCandidate `json:"sqlite_files,omitempty"`
}

// wellKnownPorts maps default server ports to engines. 6432 is pgbouncer,
// which fronts postgres.
var wellKnownPorts = map[int]Engine{
	5432: EnginePostgres,
	5433: EnginePostgres,
	6432: EnginePostgres,
	3306: EngineMySQL,
	3307: EngineMySQL,
}

// DiscoverConnections filters active sockets down to ones whose remote port
// is a well-known database server port.
func DiscoverConnections(conns []netstate.Connection) []Candidate {
	var out []Candidate
	for _, c := range conns {
		port, ok := remotePort(c.RemoteAddr)
		if !ok {
			continue
		}
		engine, ok := wellKnownPorts[port]
		if !ok {
			continue
		}
		out = append(out, Candidate{
			Engine:     engine,
			PID:        c.PID,
			Command:    c.Command,
			RemoteAddr: c.RemoteAddr,
			Port:       port,
			State:      c.State,
		})
	}
	return out
}

// remotePort extracts the port from "host:port" or "[v6::addr]:port".
func remotePort(addr string) (int, bool) {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 || idx == len(addr)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(addr[idx+1:])
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

// connectionEnvVars is the allowlist of env vars inspected for connection
// details — never full os.Environ(). Secret values are redacted on capture.
var connectionEnvVars = []struct {
	name   string
	engine Engine
	secret bool
}{
	{"DATABASE_URL", "", false}, // engine inferred from scheme
	{"POSTGRES_URL", EnginePostgres, false},
	{"POSTGRES_DSN", EnginePostgres, false},
	{"PGHOST", EnginePostgres, false},
	{"PGPORT", EnginePostgres, false},
	{"PGDATABASE", EnginePostgres, false},
	{"PGUSER", EnginePostgres, false},
	{"PGPASSWORD", EnginePostgres, true},
	{"PGSSLMODE", EnginePostgres, false},
	{"MYSQL_HOST", EngineMySQL, false},
	{"MYSQL_TCP_PORT", EngineMySQL, false},
	{"MYSQL_PWD", EngineMySQL, true},
}

// DiscoverEnv reads the connection env-var allowlist through getenv (a seam
// for tests; pass os.Getenv in production).
func DiscoverEnv(getenv func(string) string) []EnvHint {
	var out []EnvHint
	for _, v := range connectionEnvVars {
		value := getenv(v.name)
		if value == "" {
			continue
		}
		hint := EnvHint{Name: v.name, Engine: v.engine}
		switch {
		case v.secret:
			hint.Value = redacted
		case strings.Contains(value, "://"):
			hint.Value = RedactDSN(value)
			hint.Engine = engineFromScheme(value)
		default:
			hint.Value = value
		}
		out = append(out, hint)
	}
	return out
}

func engineFromScheme(dsn string) Engine {
	scheme, _, ok := strings.Cut(dsn, "://")
	if !ok {
		return ""
	}
	switch strings.ToLower(scheme) {
	case "postgres", "postgresql":
		return EnginePostgres
	case "mysql":
		return EngineMySQL
	case "sqlite", "file":
		return EngineSQLite
	default:
		return ""
	}
}
