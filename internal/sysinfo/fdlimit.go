package sysinfo

import (
	"strconv"
	"strings"
)

// FDLimit is the per-process file-descriptor limit macOS launches processes
// under (soft = current, hard = ceiling). It complements the system-wide
// kern.maxfiles sysctl and per-process open_fds counts.
type FDLimit struct {
	Soft          int  `json:"soft,omitempty"`
	Hard          int  `json:"hard,omitempty"`
	HardUnlimited bool `json:"hard_unlimited,omitempty"`
}

// CollectFDLimit runs `launchctl limit maxfiles` and parses the soft/hard
// file-descriptor limits. Output looks like:
//
//	maxfiles    256            unlimited
//
// The collector is defensive: missing/empty/error output, a line that does
// not start with "maxfiles", or unparseable columns yield the zero value.
// A soft or hard column of "unlimited" leaves the numeric field 0; the hard
// case additionally sets HardUnlimited.
func CollectFDLimit(run CmdRunner) FDLimit {
	out, err := run("launchctl", "limit", "maxfiles")
	if err != nil {
		return FDLimit{}
	}
	return parseFDLimit(string(out))
}

func parseFDLimit(out string) FDLimit {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "maxfiles" {
			continue
		}
		var fd FDLimit
		if v, ok := parseLimitValue(fields[1]); ok {
			fd.Soft = v
		}
		if fields[2] == "unlimited" {
			fd.HardUnlimited = true
		} else if v, ok := parseLimitValue(fields[2]); ok {
			fd.Hard = v
		}
		return fd
	}
	return FDLimit{}
}

// parseLimitValue returns the numeric value of a limit column, reporting
// false for "unlimited" or anything that isn't a non-negative integer.
func parseLimitValue(s string) (int, bool) {
	if s == "unlimited" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
