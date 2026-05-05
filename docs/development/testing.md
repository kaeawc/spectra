# Testing

## Test layout

All tests live alongside the code they exercise:

- `internal/detect/detect_test.go` — unit tests for every collector and
  classifier. Builds synthetic `.app` bundles in `t.TempDir()` and
  drives `Detect()` against them.

## Running

```bash
make test                                    # full suite, race detector off
go test ./... -count=1 -race                 # race detector on (CI)
go test -v ./internal/detect/                # verbose, single package
go test -v ./internal/detect/ -run TestDetectElectron
```

## Synthetic bundles

`internal/detect/detect_test.go` exposes `makeBundle(t, name)` which
builds a minimal `.app` skeleton at `t.TempDir()/<name>.app` with:

- `Contents/` directory tree
- A minimal XML `Info.plist` setting `CFBundleExecutable` to `main`
- A placeholder binary at `Contents/MacOS/main`

Tests then add the markers they care about:

```go
app := makeBundle(t, "FakeElectron")
os.MkdirAll(filepath.Join(app, "Contents/Frameworks/Electron Framework.framework"), 0o755)
touch(t, filepath.Join(app, "Contents/Resources/app.asar"))

r, _ := Detect(app)
if r.UI != "Electron" { t.Errorf(...) }
```

This style lets every Layer 1 marker get a fast unit test with no
filesystem dependencies on `/Applications`.

## What we test today

- Layer 1 markers: Electron, Flutter, Qt, Compose Desktop, Eclipse RCP
- Layer 3: Rust binary detection from synthetic panic strings
- Metadata extraction (Info.plist parsing)
- npm package enumeration (with scoped packages)
- Network endpoint extraction (regex correctness)
- Helper / XPC / plugin enumeration
- Privacy description parsing
- Bundle ID prefix and SQL safety (`internal/bundleid.Valid`)
- Wrapper following on small main executables
- Error path: rejecting non-`.app` paths

## What we deliberately do not test

- **Real `/Applications` apps** — too much install-state-dependent.
  CI smoke-tests against `/System/Applications/Calculator.app` and
  `/System/Applications/Chess.app` which exist on every macOS runner.
- **TCC.db reads** — requires a real SQLite database with an
  applicable `client` row. The validation gate (`internal/bundleid.Valid`)
  is unit-tested; the integration is smoke-tested end-to-end.

## Running tests in CI

The GitHub Actions workflow runs:

```bash
go vet ./...
go build -o spectra ./cmd/spectra/
go test ./... -count=1 -race
./spectra /System/Applications/Calculator.app
./spectra --json /System/Applications/Chess.app | python3 -m json.tool
```

See `.github/workflows/ci.yml`.

## Adding a test for a new framework

When you add detection for a new framework, the test pattern:

1. Build a synthetic bundle.
2. Add the minimal markers your detector looks for.
3. Call `Detect(app)`, assert UI/Runtime/Language/Confidence.

```go
func TestDetectFooBar(t *testing.T) {
    app := makeBundle(t, "FakeFoo")
    touch(t, filepath.Join(app, "Contents/Frameworks/FooBar.framework/FooBar"))

    r, _ := Detect(app)
    if r.UI != "FooBar" {
        t.Errorf("UI = %q, want FooBar", r.UI)
    }
}
```
