package process

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// linuxClkTck is the kernel's USER_HZ. sysconf(_SC_CLK_TCK) is 100 on
// effectively every Linux target; reading it needs cgo (disabled for the
// static Linux build), so it is a constant here and a parameter to the
// pure helpers below for testability.
const linuxClkTck int64 = 100

// LinuxClkTck is linuxClkTck for callers that diff cumulative CPU ticks.
const LinuxClkTck = linuxClkTck

// ParseLinuxCPUTimes returns utime+stime and the start time of a
// /proc/<pid>/stat line, both in clock ticks.
func ParseLinuxCPUTimes(data []byte) (cpuTicks, startTicks int64, ok bool) {
	st, ok := parseLinuxStat(data)
	if !ok {
		return 0, 0, false
	}
	return st.utimeTicks + st.stimeTicks, st.starttimeTicks, true
}

// collectLinuxProcs reads running processes from a procfs rooted at root
// (normally "/proc"). It is pure Go and root-injectable so the parsing is
// testable on any host against a fixture tree. Individual unreadable
// entries (a pid that exits mid-scan, a hidepid mount) are skipped rather
// than failing the whole collection.
func collectLinuxProcs(root string, now func() time.Time) []Info {
	return collectLinuxProcsWith(root, now, 0, nil)
}

// collectLinuxProcsWith is collectLinuxProcs with the page size (0 means
// os.Getpagesize()) and the uid lookup (nil means os/user) injectable.
func collectLinuxProcsWith(root string, now func() time.Time, pageSize int64, lookup func(int) (string, error)) []Info {
	if now == nil {
		now = time.Now
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	bootTime, haveBoot := readBtime(root)
	if pageSize <= 0 {
		pageSize = int64(os.Getpagesize())
	}
	if lookup == nil {
		lookup = lookupOSUser
	}
	users := userCache{names: map[int]string{}, lookup: lookup}
	at := now()

	var out []Info
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		info, ok := readLinuxProc(root, pid, bootTime, haveBoot, at, pageSize, &users)
		if ok {
			out = append(out, info)
		}
	}
	return out
}

// readBtime returns boot time from the "btime <epoch>" line of
// <root>/stat. The second return is false when it cannot be read.
func readBtime(root string) (time.Time, bool) {
	data, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return time.Time{}, false
	}
	return parseBtime(data)
}

func parseBtime(data []byte) (time.Time, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			return time.Unix(secs, 0), true
		}
	}
	return time.Time{}, false
}

func readLinuxProc(root string, pid int, bootTime time.Time, haveBoot bool, at time.Time, pageSize int64, users *userCache) (Info, bool) {
	statBytes, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return Info{}, false
	}
	st, ok := parseLinuxStat(statBytes)
	if !ok {
		return Info{}, false
	}

	pageKiB := pageSize / 1024
	if pageKiB <= 0 {
		pageKiB = 4
	}
	info := Info{
		PID:         pid,
		PPID:        st.ppid,
		Command:     st.comm,
		RSSKiB:      st.rssPages * pageKiB,
		VSizeKiB:    st.vsizeBytes / 1024,
		ThreadCount: st.numThreads,
	}

	if cmdline := readCmdline(root, pid); cmdline != "" {
		info.FullCommandLine = cmdline
	} else {
		// Kernel threads have an empty cmdline; show the comm in brackets
		// the way ps does.
		info.FullCommandLine = "[" + st.comm + "]"
	}

	info.ExecutablePath, info.ExecutableDeleted = readExeLink(root, pid)

	if uid, ok := readProcUID(root, pid); ok {
		info.UID = uid
		info.User = users.name(uid)
	}

	if haveBoot && st.starttimeTicks > 0 {
		start := bootTime.Add(time.Duration(st.starttimeTicks) * time.Second / time.Duration(linuxClkTck))
		info.StartTime = start
		if elapsed := at.Sub(start).Seconds(); elapsed > 0 {
			cpuSecs := float64(st.utimeTicks+st.stimeTicks) / float64(linuxClkTck)
			info.CPUPct = 100 * cpuSecs / elapsed
		}
	}
	return info, true
}

// linuxStat holds the /proc/<pid>/stat fields Spectra uses.
type linuxStat struct {
	comm           string
	ppid           int
	utimeTicks     int64
	stimeTicks     int64
	numThreads     int
	starttimeTicks int64
	vsizeBytes     int64
	rssPages       int64
}

// parseLinuxStat parses /proc/<pid>/stat. The comm field (position 2) is
// wrapped in parentheses and may itself contain spaces or parentheses, so
// the numeric fields are taken from after the final ')'. Field numbers are
// the 1-based man-proc positions; indexing below converts to the 0-based
// slice of tokens that begins at position 3 (state).
func parseLinuxStat(data []byte) (linuxStat, bool) {
	s := string(data)
	open := strings.IndexByte(s, '(')
	end := strings.LastIndexByte(s, ')')
	if open < 0 || end < 0 || end < open {
		return linuxStat{}, false
	}
	comm := s[open+1 : end]
	rest := strings.Fields(s[end+1:])
	// rest[0] is field 3 (state); field N maps to rest[N-3].
	const (
		idxPPID      = 4 - 3
		idxUtime     = 14 - 3
		idxStime     = 15 - 3
		idxThreads   = 20 - 3
		idxStarttime = 22 - 3
		idxVsize     = 23 - 3
		idxRSS       = 24 - 3
	)
	if len(rest) <= idxRSS {
		return linuxStat{}, false
	}
	atoi := func(i int) int64 { n, _ := strconv.ParseInt(rest[i], 10, 64); return n }
	// ppid and numThreads are int-width fields; parse them with Atoi to
	// avoid a narrowing int64→int conversion (CWE-190).
	atoiInt := func(i int) int { n, _ := strconv.Atoi(rest[i]); return n }
	return linuxStat{
		comm:           comm,
		ppid:           atoiInt(idxPPID),
		utimeTicks:     atoi(idxUtime),
		stimeTicks:     atoi(idxStime),
		numThreads:     atoiInt(idxThreads),
		starttimeTicks: atoi(idxStarttime),
		vsizeBytes:     atoi(idxVsize),
		rssPages:       atoi(idxRSS),
	}, true
}

// deletedSuffix is what the kernel appends to /proc/<pid>/exe once the file
// has been unlinked or replaced.
const deletedSuffix = " (deleted)"

// readExeLink returns the kernel-reported executable of pid. Kernel threads
// and other users' processes (without privilege) have no readable link and
// return "".
func readExeLink(root string, pid int) (string, bool) {
	target, err := os.Readlink(filepath.Join(root, strconv.Itoa(pid), "exe"))
	if err != nil || target == "" {
		return "", false
	}
	if trimmed, ok := strings.CutSuffix(target, deletedSuffix); ok {
		return trimmed, true
	}
	return target, false
}

func readCmdline(root string, pid int) string {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return ""
	}
	// argv entries are NUL-separated, with a trailing NUL.
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	return strings.Join(args, " ")
}

// readProcUID returns the real UID from the "Uid:" line of
// /proc/<pid>/status (the first of its four columns).
func readProcUID(root string, pid int) (int, bool) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false
	}
	return parseStatusUID(data)
}

func parseStatusUID(data []byte) (int, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				return 0, false
			}
			uid, err := strconv.Atoi(fields[0])
			if err != nil {
				return 0, false
			}
			return uid, true
		}
	}
	return 0, false
}

// userCache resolves uids to user names, memoized per collection. A failed
// lookup or an empty name falls back to the numeric uid (the pure-Go
// /etc/passwd read under CGO_ENABLED=0 misses directory-service users).
type userCache struct {
	names  map[int]string
	lookup func(int) (string, error)
}

func (c *userCache) name(uid int) string {
	if name, ok := c.names[uid]; ok {
		return name
	}
	name := strconv.Itoa(uid)
	if u, err := c.lookup(uid); err == nil && u != "" {
		name = u
	}
	c.names[uid] = name
	return name
}

func lookupOSUser(uid int) (string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", err
	}
	return u.Username, nil
}
