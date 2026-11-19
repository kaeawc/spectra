package owner

import (
	"regexp"
	"strconv"
	"strings"
)

var ssUser = regexp.MustCompile(`\("([^"]*)",pid=(\d+),fd=(\d+)\)`)

// parseSS extracts holders of a local port from `ss -H` output. hidden is
// true when a matching socket carried no process info, which ss omits for
// other users' sockets when not root.
func parseSS(out []byte, proto string, port int) (holders []Holder, hidden bool) {
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		netid, state, local, peer := f[0], f[1], f[4], f[5]
		if (proto != "" && netid != proto) || (netid != "tcp" && netid != "udp") || portOf(local) != port {
			continue
		}
		hold := HoldConnected
		if state == "LISTEN" || (state == "UNCONN" && netid == "udp") {
			hold = HoldListen
		}
		users := ssUser.FindAllStringSubmatch(line, -1)
		if len(users) == 0 {
			hidden = true
			continue
		}
		detail := netid + " " + local
		if hold == HoldConnected {
			detail += "->" + peer
		}
		for _, u := range users {
			pid, _ := strconv.Atoi(u[2])
			holders = append(holders, Holder{PID: pid, Command: u[1], Hold: hold, Detail: detail + " fd " + u[3]})
		}
	}
	return holders, hidden
}
