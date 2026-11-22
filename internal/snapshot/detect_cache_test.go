package snapshot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// hermeticDetectOpts pins the macOS app-inspection path and gives it a runner
// that answers no commands, so these cache tests never shell out to
// plutil/otool and behave the same on any host OS.
func hermeticDetectOpts() detect.Options {
	return detect.Options{OS: hostos.Darwin, Runner: proc.NewFake()}
}

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
	r, err := detectWithCache(app, hermeticDetectOpts(), nil, nil, false)
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
	r, err := detectWithCache(app, hermeticDetectOpts(), st, nil, false)
	if err != nil {
		t.Fatalf("detectWithCache: %v", err)
	}
	if r.BundleID != "sentinel-bundle-id" {
		t.Errorf("BundleID = %q, want sentinel-bundle-id (cache not hit)", r.BundleID)
	}
}

func TestDetectWithCacheHitRefreshesStorage(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)
	app := makeTestBundle(t, dir, "StoreApp", "com.store.app", "StoreApp")
	st := cache.NewShardedStore(filepath.Join(dir, "cache", "v1"), "detect")
	key := detectCacheKey(app)
	if key == nil {
		t.Fatal("detectCacheKey returned nil for valid bundle")
	}
	stale := detect.Result{Path: app, BundleID: "com.store.app", Storage: &detect.StorageFootprint{Caches: 1, Total: 1}}
	blob, _ := json.Marshal(stale)
	if err := st.Put(key, blob); err != nil {
		t.Fatalf("Put: %v", err)
	}
	caches := filepath.Join(home, "Library", "Caches", "com.store.app")
	if err := os.MkdirAll(caches, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(caches, "blob"), make([]byte, 10000), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := detectWithCache(app, hermeticDetectOpts(), st, nil, false)
	if err != nil || r.Storage == nil || r.Storage.Caches != 1 {
		t.Fatalf("without refresh: storage %+v err %v, want the cached footprint", r.Storage, err)
	}
	r, err = detectWithCache(app, hermeticDetectOpts(), st, nil, true)
	if err != nil || r.Storage == nil || r.Storage.Caches < 10000 {
		t.Fatalf("with refresh: storage %+v err %v, want the live footprint", r.Storage, err)
	}
}

// TestDetectWithCacheHitRefreshesLiveFields checks that a hit never replays
// the cached processes, uptime, login items or TCC grants: they describe the
// running system, which the content-only key does not follow.
func TestDetectWithCacheHitRefreshesLiveFields(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home) // the user TCC.db path follows $HOME
	app := makeTestBundle(t, dir, "LiveFields", "com.livefields.app", "LiveFields")
	st := cache.NewShardedStore(filepath.Join(dir, "cache", "v1"), "detect")
	key := detectCacheKey(app)
	if key == nil {
		t.Fatal("detectCacheKey returned nil for valid bundle")
	}
	oldStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stale := detect.Result{
		Path: app, BundleID: "com.livefields.app",
		RunningProcesses:   []detect.ProcessInfo{{PID: 1, RSSKiB: 1, Command: "Contents/MacOS/Old", StartTime: oldStart}},
		AppStartedAt:       &oldStart,
		AppUptimeSeconds:   1,
		LoginItems:         []detect.LoginItem{{Label: "com.livefields.stale"}},
		GrantedPermissions: []string{"Stale"},
	}
	blob, _ := json.Marshal(stale)
	if err := st.Put(key, blob); err != nil {
		t.Fatalf("Put: %v", err)
	}

	agents := filepath.Join(home, "Library", "LaunchAgents")
	tccDir := filepath.Join(home, "Library", "Application Support", "com.apple.TCC")
	for _, d := range []string{agents, tccDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(agents, "com.livefields.helper.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tccDir, "TCC.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 11, 0, 0, 0, time.UTC)
	runner := proc.NewFake().
		On(proc.MatchPrefix("ps"), proc.Response{Result: proc.Result{
			Stdout: []byte("4242 9000 Mon Nov 16 10:00:00 2026 " + app + "/Contents/MacOS/LiveFields\n"),
		}}).
		On(proc.MatchPrefix("sqlite3"), proc.Response{Result: proc.Result{Stdout: []byte("kTCCServiceCamera\n")}})
	opts := detect.Options{OS: hostos.Darwin, Runner: runner, Home: home, Now: func() time.Time { return now }}

	r, err := detectWithCache(app, opts, st, nil, false)
	if err != nil {
		t.Fatalf("detectWithCache: %v", err)
	}
	if len(r.RunningProcesses) != 1 || r.RunningProcesses[0].PID != 4242 || r.RunningProcesses[0].RSSKiB != 9000 {
		t.Errorf("RunningProcesses = %+v, want the live pid 4242", r.RunningProcesses)
	}
	if r.AppUptimeSeconds != 3600 || r.AppStartedAt == nil || r.AppStartedAt.Equal(oldStart) {
		t.Errorf("uptime = %d started %v, want 3600s from the live process", r.AppUptimeSeconds, r.AppStartedAt)
	}
	if len(r.LoginItems) != 1 || r.LoginItems[0].Label != "com.livefields.helper" {
		t.Errorf("LoginItems = %+v, want the live com.livefields.helper", r.LoginItems)
	}
	if len(r.GrantedPermissions) != 1 || r.GrantedPermissions[0] != "Camera" {
		t.Errorf("GrantedPermissions = %v, want [Camera]", r.GrantedPermissions)
	}
}

// TestDetectWithCacheHitRefreshesBundleSize checks that, with refreshStorage,
// a hit after a change confined to Contents/Resources (which leaves the key
// unchanged) reports the new bundle size, while the rules path replays it.
func TestDetectWithCacheHitRefreshesBundleSize(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	app := makeTestBundle(t, dir, "SizeApp", "com.size.app", "SizeApp")
	st := cache.NewShardedStore(filepath.Join(dir, "cache", "v1"), "detect")
	first, err := detectWithCache(app, hermeticDetectOpts(), st, nil, true)
	if err != nil {
		t.Fatalf("detectWithCache: %v", err)
	}
	key := detectCacheKey(app)
	res := filepath.Join(app, "Contents", "Resources")
	if err := os.MkdirAll(res, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res, "app.asar"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if string(detectCacheKey(app)) != string(key) {
		t.Fatal("a Resources change should leave the cache key unchanged")
	}

	stale, err := detectWithCache(app, hermeticDetectOpts(), st, nil, false)
	if err != nil || stale.BundleSizeBytes != first.BundleSizeBytes {
		t.Fatalf("rules path: size %d err %v, want the cached %d", stale.BundleSizeBytes, err, first.BundleSizeBytes)
	}
	r, err := detectWithCache(app, hermeticDetectOpts(), st, nil, true)
	if err != nil {
		t.Fatalf("detectWithCache: %v", err)
	}
	if r.BundleSizeBytes < first.BundleSizeBytes+1<<20 || r.ApparentSizeBytes < first.ApparentSizeBytes+1<<20 {
		t.Errorf("size = %d/%d, want at least 1 MiB over the cached %d/%d",
			r.BundleSizeBytes, r.ApparentSizeBytes, first.BundleSizeBytes, first.ApparentSizeBytes)
	}
}

func TestDetectWithCacheMissRunsLiveDetect(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "LiveApp", "com.live.app", "LiveApp")

	storeDir := filepath.Join(dir, "cache", "v1")
	st := cache.NewShardedStore(storeDir, "detect")

	// Empty cache → live detect runs and result is stored.
	r, err := detectWithCache(app, hermeticDetectOpts(), st, nil, false)
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

func TestDetectWithCacheUsesAsyncWriter(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "AsyncApp", "com.async.app", "AsyncApp")

	storeDir := filepath.Join(dir, "cache", "v1")
	st := cache.NewShardedStore(storeDir, "detect")
	writer := cache.NewAsyncWriter(st, 8, 1)
	defer writer.Close()

	if _, err := detectWithCache(app, hermeticDetectOpts(), st, writer, false); err != nil {
		t.Fatalf("detectWithCache: %v", err)
	}
	writer.Close()

	key := detectCacheKey(app)
	if _, ok, _ := st.Get(key); !ok {
		t.Fatal("result was not stored by async writer")
	}
	if writer.Queued.Load() == 0 {
		t.Fatal("async writer was not used")
	}
}

// The key is content-only, so an identical copy hits the original's entry;
// the hit must describe the bundle that was asked about.
func TestDetectWithCacheHitRefreshesPathAndSignature(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "Copy", "com.copy.app", "Copy")
	st := cache.NewShardedStore(filepath.Join(dir, "cache", "v1"), "detect")
	stale := detect.Result{Path: "/Applications/Original.app", BundleID: "com.copy.app", CDHash: "stale", GatekeeperLikely: "accepted", GatekeeperMethod: detect.GatekeeperMethodSignature}
	blob, _ := json.Marshal(stale)
	if err := st.Put(detectCacheKey(app), blob); err != nil {
		t.Fatal(err)
	}
	opts := hermeticDetectOpts()
	opts.Runner = proc.NewFake().On(proc.MatchPrefix("codesign", "-dv"), proc.Response{Result: proc.Result{
		Stderr: []byte(app + ": code object is not signed at all\n"),
	}})
	r, err := detectWithCache(app, opts, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != app || r.CDHash != "" || r.GatekeeperLikely != "" || r.GatekeeperMethod != "" {
		t.Fatalf("hit = path %q cdhash %q likely %q method %q, want the asked-about unsigned copy", r.Path, r.CDHash, r.GatekeeperLikely, r.GatekeeperMethod)
	}
}

func TestDetectWithCacheNeverPersistsSpctlTimeout(t *testing.T) {
	dir := t.TempDir()
	app := makeTestBundle(t, dir, "SlowApp", "com.slow.app", "SlowApp")
	st := cache.NewShardedStore(filepath.Join(dir, "cache", "v1"), "detect")

	opts := hermeticDetectOpts()
	opts.Runner = proc.NewFake().On(proc.MatchPrefix("spctl"), proc.Response{Err: context.DeadlineExceeded})
	opts.AssessGatekeeper = true
	opts.Gatekeeper = detect.NewGatekeeperAssessor(time.Second, time.Hour, nil)

	r, err := detectWithCache(app, opts, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.GatekeeperError != detect.GatekeeperErrorTimeout || r.GatekeeperStatus != "" {
		t.Fatalf("live result = status %q err %q, want timeout", r.GatekeeperStatus, r.GatekeeperError)
	}
	blob, ok, _ := st.Get(detectCacheKey(app))
	if !ok {
		t.Fatal("result was not stored")
	}
	var stored detect.Result
	if err := json.Unmarshal(blob, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.GatekeeperError != "" || stored.GatekeeperStatus != "" {
		t.Fatalf("persisted spctl outcome: status %q err %q", stored.GatekeeperStatus, stored.GatekeeperError)
	}

	// A later assessing call re-runs spctl rather than replaying the miss.
	opts.Runner = proc.NewFake().On(proc.MatchPrefix("spctl"), proc.Response{Result: proc.Result{
		Stderr: []byte(app + ": accepted\nsource=Notarized Developer ID\n"),
	}})
	r, err = detectWithCache(app, opts, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.GatekeeperStatus != "accepted" || r.GatekeeperMethod != detect.GatekeeperMethodSpctl {
		t.Fatalf("cache hit = status %q method %q, want accepted via spctl", r.GatekeeperStatus, r.GatekeeperMethod)
	}
}
