package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/proc"
)

// detectWithCache runs detect.DetectWith, returning a cached result when
// available. The cache key is SHA-256 of Info.plist bytes + first 64 KiB of
// the main executable, so the entry is invalidated automatically when either
// file changes.
//
// The persisted entry never carries a spctl verdict: a timeout must not be
// replayed as "unassessed" forever, and a real verdict is only trusted for
// the detect package's path+CDHash-keyed TTL. Because the key is content
// only, an identical copy at another path or a re-signed bundle can hit
// another bundle's entry, so a hit re-reads the signature for appPath (one
// `codesign -dv`; the seal check is cached) and, when opts.AssessGatekeeper
// is set, re-assesses through the in-process verdict cache.
//
// A hit also recollects the runtime fields (processes, uptime, login items,
// TCC grants), which describe the running system, not the bundle. When
// refreshStorage is set it also re-measures the bundle size and the app's
// ~/Library footprint, which the content-only key cannot track either.
//
// store may be nil, in which case the function falls through to a live Detect.
func detectWithCache(appPath string, opts detect.Options, store *cache.ShardedStore, writer *cache.AsyncWriter, refreshStorage bool) (detect.Result, error) {
	if store == nil {
		return detect.DetectWith(appPath, opts)
	}

	key := detectCacheKey(appPath)
	if key != nil {
		if blob, ok, _ := store.Get(key); ok {
			var r detect.Result
			if err := json.Unmarshal(blob, &r); err == nil {
				detect.RefreshSignature(&r, appPath, opts)
				detect.RefreshLive(&r, appPath, opts)
				if refreshStorage {
					detect.RefreshBundleSize(&r, appPath)
					detect.RefreshStorage(&r, appPath)
				}
				return r, nil
			}
		}
	}

	r, err := detect.DetectWith(appPath, opts)
	if err == nil && key != nil {
		if blob, jerr := json.Marshal(detect.WithoutGatekeeperAssessment(r)); jerr == nil {
			if writer != nil {
				writer.Write(key, blob)
			} else {
				_ = store.Put(key, blob)
			}
		}
	}
	return r, err
}

// detectCacheSchema salts the key so entries written before the Result
// shape changed (spctl verdicts persisted, no CDHash or signature
// inference) are not served.
var detectCacheSchema = []byte("detect-v2\x00")

// detectCacheKey returns the cache key for appPath, or nil if the key cannot
// be derived (e.g. Info.plist missing or unreadable).
func detectCacheKey(appPath string) []byte {
	plist := filepath.Join(appPath, "Contents", "Info.plist")
	plistBytes, err := os.ReadFile(plist)
	if err != nil {
		return nil
	}

	var exeBytes []byte
	if name := plistExecutableName(plist); name != "" {
		exePath := filepath.Join(appPath, "Contents", "MacOS", name)
		if f, err := os.Open(exePath); err == nil {
			buf := make([]byte, 64*1024)
			n, _ := f.Read(buf)
			f.Close()
			exeBytes = buf[:n]
		}
	}

	return cache.Key(detectCacheSchema, plistBytes, exeBytes)
}

// plistExecutableName reads the CFBundleExecutable value from a plist file.
// Returns "" on any error.
func plistExecutableName(plistPath string) string {
	out, err := proc.BoundedOutput("plutil", "-extract", "CFBundleExecutable", "raw", "-o", "-", plistPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
