package daemon

import (
	"fmt"
	"path/filepath"
)

type Paths struct{ Dir, Socket, Lock, PID, Log string }

func DefaultPaths(env func(string) string, home func() (string, error), goos string) (Paths, error) {
	if env == nil {
		env = func(string) string { return "" }
	}
	dir := env("SPECTRA_DAEMON_DIR")
	h := ""
	if dir == "" || goos != "linux" || env("XDG_STATE_HOME") == "" {
		var err error
		h, err = home()
		if err != nil {
			return Paths{}, fmt.Errorf("daemon paths: home: %w", err)
		}
	}
	if dir == "" {
		dir = filepath.Join(h, ".spectra")
		if goos == "linux" && env("XDG_RUNTIME_DIR") != "" {
			dir = filepath.Join(env("XDG_RUNTIME_DIR"), "spectra")
		}
	}
	p := Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	if len([]byte(p.Socket)) >= 104 {
		return Paths{}, fmt.Errorf("daemon socket path exceeds 103 bytes: %s", p.Socket)
	}
	if goos == "linux" {
		state := env("XDG_STATE_HOME")
		if state == "" {
			state = filepath.Join(h, ".local", "state")
		}
		p.Log = filepath.Join(state, "spectra", "daemon.jsonl")
	} else {
		p.Log = filepath.Join(h, "Library", "Logs", "Spectra", "daemon.jsonl")
	}
	if override := env("SPECTRA_DAEMON_LOG"); override != "" {
		p.Log = override
	}
	return p, nil
}
