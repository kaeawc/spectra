# Storage footprint

macOS apps spread persistent state across half a dozen directories
under `~/Library`. Spectra sums them per-bundle so the real on-disk
cost of an app is one number, not eight separate `du` invocations.

## Locations probed

Each location is checked under both the bundle ID and the human
display name (apps register state under either, sometimes both):

| Path | Typical contents |
|---|---|
| `~/Library/Application Support/<key>` | App data, project files, indexes |
| `~/Library/Caches/<key>` | Disposable caches |
| `~/Library/Containers/<key>` | Sandboxed apps (everything lives here) |
| `~/Library/Group Containers/<key>` | Cross-app shared data |
| `~/Library/HTTPStorages/<key>` | Cookies, HSTS state |
| `~/Library/WebKit/<key>` | WKWebView local state |
| `~/Library/Logs/<key>` | App logs |
| `~/Library/Preferences/<key>.plist` | Preferences (single file) |

`<key>` is tried as both the bundle ID (`com.anthropic.claudefordesktop`)
and the display name (`Claude`). Both contribute to the total when
present.

## Sparse-file accuracy

A naive `fi.Size()` walk inflates dramatically when sparse files are
involved — most notoriously Docker's `~/Library/Containers/com.docker.docker`,
which holds a virtual disk image whose apparent size is hundreds of GB
but actual on-disk allocation is much smaller.

Spectra uses `Stat_t.Blocks * 512` on Darwin to report the **actual
bytes allocated**, not the apparent size:

```go
// internal/detect/syscall_darwin.go
func diskBytes(fi os.FileInfo) int64 {
    st, ok := fi.Sys().(*syscall.Stat_t)
    if !ok {
        return fi.Size()
    }
    return int64(st.Blocks) * 512
}
```

The fallback (`syscall_other.go`) returns apparent size — not used on
macOS but keeps cross-compilation clean.

### Empirical impact

- **Before the fix:** Docker storage reported as 1858.2 GB (apparent).
- **After the fix:** 33.9 GB (actual on-disk allocation).
- Claude's Application Support dropped from 23.8 GB to 11.6 GB for the
  same reason (some sparse content).

## Sample output

```
Claude
  storage: 11.6 GB total — appsupport 11.5 GB, caches 2 MB, http 488 KB,
                           logs 32 MB, prefs 1 KB
Docker
  storage: 33.9 GB total — containers 33.9 GB
Slack
  storage: 671 MB total — containers 671 MB    # sandboxed
Cursor
  storage: 97 MB total — appsupport 96 MB, caches 852 KB,
                         http 168 KB, prefs 1 KB
```

Sandboxed apps (Slack) report everything under `containers` because
that's where the sandbox bind-mounts their data. Non-sandboxed apps
spread across multiple paths.

## Implementation reference

`internal/detect/detect.go`:
- `scanStorage(appPath, bundleID) *StorageFootprint`
- `bundleSize(path) int64` (uses `diskBytes` per file)

The full output is the `Storage` field on `detect.Result`. JSON
consumers get the per-location breakdown plus paths actually found.
