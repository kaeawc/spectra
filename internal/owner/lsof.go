package owner

import (
	"bufio"
	"bytes"
	"slices"
	"strconv"
	"strings"
)

const (
	lsofPortFields = "pcLfatnPT"
	lsofPathFields = "pcLfatn"
)

type lsofRec struct {
	PID                                      int
	Command, User                            string
	FD, Type, Access, Name, Proto, TCPStatus string
}

// parseLsofF parses `lsof -F` output: one tagged field per line, a "p" line
// opening a process set and an "f" line opening a file set within it.
func parseLsofF(out []byte) []lsofRec {
	var recs []lsofRec
	var pid int
	var cmd, user string
	var cur lsofRec
	open := false
	flush := func() {
		if open {
			cur.PID, cur.Command, cur.User = pid, cmd, user
			recs = append(recs, cur)
		}
		open = false
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 2 {
			continue
		}
		val := line[1:]
		switch line[0] {
		case 'p':
			flush()
			pid, _ = strconv.Atoi(val)
			cmd, user = "", ""
		case 'c':
			cmd = val
		case 'L':
			user = val
		case 'f':
			flush()
			cur = lsofRec{FD: val}
			open = true
		case 'a':
			cur.Access = strings.TrimSpace(val)
		case 't':
			cur.Type = val
		case 'n':
			cur.Name = val
		case 'P':
			cur.Proto = strings.ToLower(val)
		case 'T':
			if st, ok := strings.CutPrefix(val, "ST="); ok {
				cur.TCPStatus = st
			}
		}
	}
	flush()
	return recs
}

// portOf returns the port of an address like "*:8080" or "[::1]:80".
func portOf(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(addr[i+1:])
	return n
}

// lsofPortHolders keeps records whose local end is the target port (lsof
// -i:PORT also matches the remote end).
func lsofPortHolders(recs []lsofRec, port int) []Holder {
	var out []Holder
	for _, r := range recs {
		if r.Type != "IPv4" && r.Type != "IPv6" {
			continue
		}
		local, _, connected := strings.Cut(r.Name, "->")
		if portOf(local) != port {
			continue
		}
		hold := HoldConnected
		if r.TCPStatus == "LISTEN" || (r.Proto == "udp" && !connected) {
			hold = HoldListen
		}
		out = append(out, Holder{PID: r.PID, User: r.User, Command: r.Command, Hold: hold, Detail: r.Proto + " " + r.Name})
	}
	return out
}

// lsofPathHolders keeps only records naming one of paths: lsof on a mount
// point reports every open file on that filesystem.
func lsofPathHolders(recs []lsofRec, paths []string) []Holder {
	var out []Holder
	for _, r := range recs {
		if !slices.Contains(paths, r.Name) {
			continue
		}
		h := Holder{PID: r.PID, User: r.User, Command: r.Command}
		switch {
		case r.FD == "cwd":
			h.Hold = HoldCwd
		case r.FD == "txt" || r.FD == "mem":
			h.Hold, h.Detail = HoldTxt, r.FD
		case r.FD != "" && r.FD[0] >= '0' && r.FD[0] <= '9':
			h.Hold, h.Mode, h.Detail = HoldFD, accessMode(r.Access), "fd "+strings.TrimRight(r.FD, "rwuRWU")
		default:
			continue
		}
		out = append(out, h)
	}
	return out
}

func accessMode(a string) string {
	switch a {
	case "r":
		return "r"
	case "w":
		return "w"
	case "u":
		return "rw"
	}
	return ""
}
