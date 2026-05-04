package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/detect"
)

// makeTestBundle creates a minimal .app directory in dir and returns its path.
func makeTestBundle(t *testing.T, dir, name, bundleID, execName string) string {
	t.Helper()
	app := filepath.Join(dir, name+".app")
	macosDir := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macosDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plistContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + bundleID + `</string>
<key>CFBundleExecutable</key><string>` + execName + `</string>
<key>CFBundleShortVersionString</key><string>1.0.0</string>
</dict></plist>`
	plistPath := filepath.Join(app, "Contents", "Info.plist")
	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a fake executable (just some bytes).
	exePath := filepath.Join(macosDir, execName)
	if err := os.WriteFile(exePath, []byte("fakebinary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestDetectCacheKeyNonNilForValidBundle(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "TestApp", "com.test.app", "TestApp")
	key := detectCacheKey(app)
	if key == nil {
		t.Error("detectCacheKey returned nil for valid bundle")
	}
	if len(key) == 0 {
		t.Error("detectCacheKey returned empty key")
	}
}

func TestDetectCacheKeyNilForMissingBundle(t *testing.T) {
	key := detectCacheKey("/nonexistent/path/Foo.app")
	if key != nil {
		t.Errorf("detectCacheKey = %v, want nil for missing bundle", key)
	}
}

func TestDetectCacheKeyChangesWhenPlistChanges(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "TestApp", "com.test.app", "TestApp")
	k1 := detectCacheKey(app)

	// Update the plist.
	plistPath := filepath.Join(app, "Contents", "Info.plist")
	original, _ := os.ReadFile(plistPath)
	updated := append(original, []byte("<!-- updated -->")...)
	_ = os.WriteFile(plistPath, updated, 0o644)

	k2 := detectCacheKey(app)
	if string(k1) == string(k2) {
		t.Error("cache key did not change after plist update")
	}
}

func TestDetectWithCacheNilStoreCallsLiveDetect(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "TestApp", "com.test.app", "TestApp")
	// nil store → falls through to live detect
	r, err := detectWithCache(app, detect.Options{}, nil)
	if err != nil {
		t.Fatalf("detectWithCache(nil store): %v", err)
	}
	if r.Path == "" {
		t.Error("result.Path should not be empty")
	}
}

func TestDetectWithCacheStoresAndRetrieves(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "CacheApp", "com.cache.app", "CacheApp")

	storeDir := filepath.Join(dir, "cache", "v1")
	st := cache.NewShardedStore(storeDir, "detect")

	// Pre-populate the cache with a sentinel result under the app's key.
	key := detectCacheKey(app)
	if key == nil {
		t.Fatal("detectCacheKey returned nil for valid bundle")
	}
	sentinel := detect.Result{Path: app, BundleID: "sentinel-bundle-id"}
	blob, _ := json.Marshal(sentinel)
	if err := st.Put(key, blob); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// detectWithCache should return the sentinel without running live detect.
	r, err := detectWithCache(app, detect.Options{}, st)
	if err != nil {
		t.Fatalf("detectWithCache: %v", err)
	}
	if r.BundleID != "sentinel-bundle-id" {
		t.Errorf("BundleID = %q, want sentinel-bundle-id (cache not hit)", r.BundleID)
	}
}

func TestDetectWithCacheMissRunsLiveDetect(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "LiveApp", "com.live.app", "LiveApp")

	storeDir := filepath.Join(dir, "cache", "v1")
	st := cache.NewShardedStore(storeDir, "detect")

	// Empty cache → live detect runs and result is stored.
	r, err := detectWithCache(app, detect.Options{}, st)
	if err != nil {
		t.Fatalf("detectWithCache: %v", err)
	}
	if r.Path == "" {
		t.Error("live detect result has empty Path")
	}

	// Second call → served from cache.
	key := detectCacheKey(app)
	if _, ok, _ := st.Get(key); !ok {
		t.Error("result was not stored in cache after live detect")
	}
}
