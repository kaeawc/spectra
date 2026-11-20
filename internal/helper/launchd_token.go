package helper

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// facts are everything a token is bound to besides the caller and expiry.
// The struct is comparable, so execute compares a fresh derivation with ==.
type facts struct {
	action, label         string
	plist                 string
	dev, ino              uint64
	size, mtimeNanos      int64
	sha                   string
	program, bundlePath   string
	bundleID, teamID      string
	loaded                bool
	loadedPath, loadedPrg string
	// planned is the planned argv list, JSON-encoded.
	planned string
}

type binding struct {
	uid     uint32
	facts   facts
	created time.Time
	expires time.Time
}

func (v *verified) facts() facts {
	planned, _ := json.Marshal(v.planned)
	return facts{
		action: v.action, label: v.label, plist: v.target.PlistPath,
		dev: v.plistMeta.Dev, ino: v.plistMeta.Ino, size: v.plistMeta.Size, mtimeNanos: v.plistMeta.ModTime.UnixNano(),
		sha: v.target.PlistSHA256, program: v.target.Program, bundlePath: v.target.BundlePath,
		bundleID: v.target.BundleID, teamID: v.target.TeamID,
		loaded: v.state.Loaded, loadedPath: v.state.Path, loadedPrg: v.state.Program,
		planned: string(planned),
	}
}

func (v *verified) binding(uid uint32, action string, now time.Time) binding {
	f := v.facts()
	f.action = action
	return binding{uid: uid, facts: f, created: now, expires: now.Add(tokenTTL)}
}

func encodeToken(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// tokenKey is the full SHA-256 of the token: the store never keeps the
// token itself.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenHashPrefix is the audit log's token_hash: the first 8 hex
// characters of SHA-256(token).
func tokenHashPrefix(token string) string { return tokenKey(token)[:8] }

// tokenStore holds live tokens in memory only; a helper restart drops them.
type tokenStore struct {
	mu      sync.Mutex
	entries map[string]binding
}

func newTokenStore() *tokenStore { return &tokenStore{entries: map[string]binding{}} }

// put stores b under key, dropping expired tokens and, when the caller
// already holds maxLiveTokens, evicting its oldest.
func (t *tokenStore) put(key string, b binding, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.entries {
		if !now.Before(e.expires) {
			delete(t.entries, k)
		}
	}
	for t.countFor(b.uid) >= maxLiveTokens {
		oldestKey := ""
		var oldest time.Time
		for k, e := range t.entries {
			if e.uid == b.uid && (oldestKey == "" || e.created.Before(oldest)) {
				oldestKey, oldest = k, e.created
			}
		}
		delete(t.entries, oldestKey)
	}
	t.entries[key] = b
}

func (t *tokenStore) countFor(uid uint32) int {
	n := 0
	for _, e := range t.entries {
		if e.uid == uid {
			n++
		}
	}
	return n
}

// take removes and returns the binding for key.
func (t *tokenStore) take(key string) (binding, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.entries[key]
	delete(t.entries, key)
	return b, ok
}

func (t *tokenStore) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// slidingLimiter is a per-uid sliding-window rate limit.
type slidingLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[uint32][]time.Time
}

func newSlidingLimiter(limit int, window time.Duration) *slidingLimiter {
	return &slidingLimiter{max: limit, window: window, seen: map[uint32][]time.Time{}}
}

func (l *slidingLimiter) allow(uid uint32, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	keep := l.seen[uid][:0]
	for _, ts := range l.seen[uid] {
		if ts.After(cutoff) {
			keep = append(keep, ts)
		}
	}
	if len(keep) >= l.max {
		l.seen[uid] = keep
		return false
	}
	l.seen[uid] = append(keep, now)
	return true
}
