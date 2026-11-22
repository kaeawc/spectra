package detect

import (
	"io/fs"
	"os"
	"sync"

	"github.com/kaeawc/spectra/internal/fsutil"
)

// launchPlistMemoMax bounds a memo; the launchd directories hold tens of
// plists, so reaching it means something unusual and the memo starts over.
const launchPlistMemoMax = 512

// sharedLaunchPlists serves every detector that runs the real plutil. Login
// item attribution converts each launchd plist once per inspected app, so a
// run over N apps would otherwise exec plutil N times per plist.
var sharedLaunchPlists = newLaunchPlistMemo(os.Stat)

// launchPlistMemo memoizes the xml1 conversion of launchd plists keyed by
// path and file identity (size, mtime, inode, ctime), so a rewritten or
// replaced plist is converted again on its next lookup. Concurrent lookups
// of one version share a single conversion.
type launchPlistMemo struct {
	stat func(string) (fs.FileInfo, error)

	mu sync.Mutex
	m  map[string]*launchPlistFlight
}

type launchPlistVersion struct {
	size, mtime int64
	id          fsutil.FileID
}

type launchPlistFlight struct {
	version launchPlistVersion
	done    chan struct{}
	xml     []byte
	err     error
}

// launchPlistXML returns the `plutil -convert xml1` form of a launchd plist,
// served from d.launch when the file is unchanged since its last conversion.
func (d detector) launchPlistXML(path string) ([]byte, error) {
	convert := func(p string) ([]byte, error) {
		return d.output("plutil", "-convert", "xml1", "-o", "-", p)
	}
	if d.launch == nil {
		return convert(path)
	}
	return d.launch.xml(path, convert)
}

func newLaunchPlistMemo(stat func(string) (fs.FileInfo, error)) *launchPlistMemo {
	return &launchPlistMemo{stat: stat, m: map[string]*launchPlistFlight{}}
}

// xml returns convert(path), reusing a successful result for an unchanged
// file. A file that cannot be stat'ed is converted every time.
func (c *launchPlistMemo) xml(path string, convert func(string) ([]byte, error)) ([]byte, error) {
	fi, err := c.stat(path)
	if err != nil {
		return convert(path)
	}
	v := launchPlistVersion{size: fi.Size(), mtime: fi.ModTime().UnixNano(), id: fsutil.Identity(fi)}
	c.mu.Lock()
	if f, ok := c.m[path]; ok && f.version == v {
		c.mu.Unlock()
		<-f.done
		return f.xml, f.err
	}
	if len(c.m) >= launchPlistMemoMax {
		c.m = map[string]*launchPlistFlight{}
	}
	f := &launchPlistFlight{version: v, done: make(chan struct{})}
	c.m[path] = f
	c.mu.Unlock()
	f.xml, f.err = convert(path)
	if f.err != nil {
		// A failed conversion may be transient (a plutil timeout), so it is
		// retried on the next lookup rather than held until the file changes.
		c.mu.Lock()
		if c.m[path] == f {
			delete(c.m, path)
		}
		c.mu.Unlock()
	}
	close(f.done)
	return f.xml, f.err
}
