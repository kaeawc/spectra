package detect

import (
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/kaeawc/spectra/internal/fsutil"
)

// tccGrantsQuery lists every granted (client, service) pair in a TCC
// database, so one sqlite3 run serves every bundle. hex() keeps a client or
// service containing '|' or a newline from forging a row; typeof keeps the
// per-bundle filter exactly `client = '<id>'`, which never matches a BLOB.
const tccGrantsQuery = "SELECT hex(client), hex(service) FROM access WHERE auth_value >= 2 AND typeof(client) = 'text';"

// tccMemoMax bounds a memo; there are two TCC databases, so reaching it means
// something unusual and completed entries are dropped.
const tccMemoMax = 16

// sharedTCCGrants serves every detector that runs the real sqlite3. Each
// inspected app reads both TCC databases, so a run over N apps would
// otherwise exec sqlite3 2N times.
var sharedTCCGrants = newTCCMemo(os.Stat)

// tccGrants maps a TCC client to the service names granted to it, as the
// per-bundle query printed them: one entry per output line, trimmed, with
// empty lines dropped and the kTCCService prefix still present.
type tccGrants map[string][]string

// tccMemo memoizes the parsed grants of a TCC database keyed by path and the
// identity (size, mtime, inode, ctime) of both the database and its -wal
// file: TCC writes may land only in the WAL, leaving TCC.db unchanged.
// Concurrent lookups of one version share a single sqlite3 run.
type tccMemo struct {
	stat func(string) (fs.FileInfo, error)

	mu sync.Mutex
	m  map[string]*tccFlight
}

type tccFileVersion struct {
	present     bool
	size, mtime int64
	id          fsutil.FileID
}

type tccVersion struct {
	db, wal tccFileVersion
}

type tccFlight struct {
	version tccVersion
	done    chan struct{}
	grants  tccGrants
	err     error
}

func newTCCMemo(stat func(string) (fs.FileInfo, error)) *tccMemo {
	return &tccMemo{stat: stat, m: map[string]*tccFlight{}}
}

func (c *tccMemo) fileVersion(path string) (tccFileVersion, error) {
	fi, err := c.stat(path)
	if err != nil {
		return tccFileVersion{}, err
	}
	return tccFileVersion{present: true, size: fi.Size(), mtime: fi.ModTime().UnixNano(), id: fsutil.Identity(fi)}, nil
}

// grants returns read(db) parsed, reusing a successful read while neither the
// database nor its WAL changed. A database that cannot be stat'ed is read
// every time; an absent WAL is part of the version.
func (c *tccMemo) grants(db string, read func(string) ([]byte, error)) (tccGrants, error) {
	direct := func() (tccGrants, error) {
		out, err := read(db)
		if err != nil {
			return nil, err
		}
		return parseTCCGrants(out), nil
	}
	dbv, err := c.fileVersion(db)
	if err != nil {
		return direct()
	}
	walv, err := c.fileVersion(db + "-wal")
	if err != nil && !os.IsNotExist(err) {
		return direct()
	}
	v := tccVersion{db: dbv, wal: walv}
	c.mu.Lock()
	if f, ok := c.m[db]; ok && f.version == v {
		c.mu.Unlock()
		<-f.done
		return f.grants, f.err
	}
	if len(c.m) >= tccMemoMax {
		c.evictCompletedLocked()
	}
	f := &tccFlight{version: v, done: make(chan struct{})}
	c.m[db] = f
	c.mu.Unlock()
	f.grants, f.err = direct()
	if f.err != nil {
		// Typically the system database without Full Disk Access, but a
		// sqlite3 timeout or a locked database is transient, so a failure is
		// retried on the next lookup rather than held until the file changes.
		c.mu.Lock()
		if c.m[db] == f {
			delete(c.m, db)
		}
		c.mu.Unlock()
	}
	close(f.done)
	return f.grants, f.err
}

// evictCompletedLocked drops finished reads and keeps in-flight ones, so a
// lookup that arrives while a read runs still joins it. c.mu must be held.
func (c *tccMemo) evictCompletedLocked() {
	for p, f := range c.m {
		select {
		case <-f.done:
			delete(c.m, p)
		default:
		}
	}
}

// parseTCCGrants decodes tccGrantsQuery's "HEX|HEX" rows. A service is split
// into lines and trimmed the way the per-bundle query's output was, so the
// grants are identical to running that query for each client.
func parseTCCGrants(out []byte) tccGrants {
	g := tccGrants{}
	for _, row := range strings.Split(string(out), "\n") {
		hc, hs, ok := strings.Cut(strings.TrimSpace(row), "|")
		if !ok {
			continue
		}
		client, err1 := hex.DecodeString(hc)
		service, err2 := hex.DecodeString(hs)
		if err1 != nil || err2 != nil {
			continue
		}
		for _, line := range strings.Split(string(service), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				g[string(client)] = append(g[string(client)], line)
			}
		}
	}
	return g
}

func tccDatabases() []string {
	return []string{
		filepath.Join(home(), "Library", "Application Support", "com.apple.TCC", "TCC.db"),
		"/Library/Application Support/com.apple.TCC/TCC.db",
	}
}

// scanGrantedPermissions reports which privacy services the TCC databases
// grant this bundle ID. We check both the per-user database (under ~/Library)
// and the system one (under /Library); the latter requires Full Disk Access
// for the spectra binary. When auth_value is 2 (allowed) or 3 (limited /
// always-allow) the service is included. Result is a deduped, sorted slice of
// human-readable service names with the kTCCService prefix stripped.
func (d detector) scanGrantedPermissions(bundleID string) []string {
	return d.grantedPermissionsIn(tccDatabases(), bundleID)
}

func (d detector) grantedPermissionsIn(dbs []string, bundleID string) []string {
	// Only the reverse-DNS charset is accepted, as when the bundle ID was
	// interpolated into SQL; the filter now runs in Go.
	if bundleID == "" || !validBundleID(bundleID) {
		return nil
	}
	seen := map[string]struct{}{}
	for _, db := range dbs {
		if !exists(db) {
			continue
		}
		grants, err := d.tccGrants(db)
		if err != nil {
			continue // typically: SIP-protected DB, no FDA — skip silently
		}
		for _, svc := range grants[bundleID] {
			seen[strings.TrimPrefix(svc, "kTCCService")] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (d detector) tccGrants(db string) (tccGrants, error) {
	read := func(p string) ([]byte, error) {
		return d.output("sqlite3", p, tccGrantsQuery)
	}
	if d.tcc == nil {
		out, err := read(db)
		if err != nil {
			return nil, err
		}
		return parseTCCGrants(out), nil
	}
	return d.tcc.grants(db, read)
}
