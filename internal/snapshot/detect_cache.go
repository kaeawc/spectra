package snapshot

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/detect"
)

// detectWithCache runs detect.DetectWith, returning a cached result when
// available. The cache key is SHA-256 of Info.plist bytes + first 64 KiB of
// the main executable, so the entry is invalidated automatically when either
// file changes.
//
// store may be nil, in which case the function falls through to a live Detect.
func detectWithCache(appPath string, opts detect.Options, store *cache.ShardedStore, writer *cache.AsyncWriter) (detect.Result, error) {
	if store == nil {
		return detect.DetectWith(appPath, opts)
	}

	key := detectCacheKey(appPath)
	if key != nil {
		if blob, ok, _ := store.Get(key); ok {
			var r detect.Result
			if err := json.Unmarshal(blob, &r); err == nil {
				return r, nil
			}
		}
	}

	r, err := detect.DetectWith(appPath, opts)
	if err == nil && key != nil {
		if blob, jerr := json.Marshal(r); jerr == nil {
			if writer != nil {
				writer.Write(key, blob)
			} else {
				_ = store.Put(key, blob)
			}
		}
	}
	return r, err
}

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

	return cache.Key(plistBytes, exeBytes)
}

// plistExecutableName reads the CFBundleExecutable value from a plist file.
// Returns "" on any error.
func plistExecutableName(plistPath string) string {
	out, err := exec.Command("plutil", "-extract", "CFBundleExecutable", "raw", "-o", "-", plistPath).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
