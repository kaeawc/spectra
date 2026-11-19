package owner

import "time"

// How a process holds the target.
const (
	HoldListen    = "listen"
	HoldConnected = "connected"
	HoldFD        = "fd"
	HoldCwd       = "cwd"
	HoldTxt       = "txt"
	HoldProcess   = "process"
)

// Link is one ancestor in a parent chain.
type Link struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// Holder is one process holding the target.
type Holder struct {
	PID        int       `json:"pid"`
	User       string    `json:"user,omitempty"`
	Command    string    `json:"command"`
	Executable string    `json:"executable,omitempty"`
	Started    time.Time `json:"started,omitempty"`
	Hold       string    `json:"hold"`
	Mode       string    `json:"mode,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Chain      []Link    `json:"chain,omitempty"` // nearest parent first, up to pid 1
}

// Result is the answer to one owner query.
type Result struct {
	Target  Target   `json:"target"`
	Holders []Holder `json:"holders"`
	// Incomplete is set when the tool could only see part of the system
	// because Spectra is not running as root.
	Incomplete bool   `json:"incomplete,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

const rootHint = "not running as root: only this user's processes are visible, so other owners may exist; re-run with sudo"
