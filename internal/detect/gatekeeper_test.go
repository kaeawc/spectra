package detect

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// Fixtures are trimmed real `codesign -dv --verbose=4` stderr from macOS 26.
const (
	codesignDevIDStapled = `Executable=/Applications/Google Chrome.app/Contents/MacOS/Google Chrome
Identifier=com.google.Chrome
CodeDirectory v=20500 size=637 flags=0x12a00(kill,restrict,library-validation,runtime) hashes=9+7 location=embedded
CDHash=595aa36a53e9a1c89a72d52981b7eb9e0ccb70ea
Signature size=8991
Authority=Developer ID Application: Google LLC (EQHXZ8M8AV)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
Timestamp=Nov 11, 2026 at 7:23:48 PM
Notarization Ticket=stapled
TeamIdentifier=EQHXZ8M8AV
`
	codesignDevIDUnstapled = `Executable=/Applications/MemoryAnalyzer.app/Contents/MacOS/MemoryAnalyzer
CodeDirectory v=20500 size=1234 flags=0x10000(runtime) hashes=30+7 location=embedded
CDHash=050ea16c60f05ed9d31ed1ab8c8f70ba55ad2e70
Authority=Developer ID Application: Eclipse Foundation, Inc. (JCDTMS22B4)
Authority=Developer ID Certification Authority
Authority=Apple Root CA
TeamIdentifier=JCDTMS22B4
`
	codesignMAS = `Executable=/Applications/Keynote.app/Contents/MacOS/Keynote
CodeDirectory v=20500 size=9999 flags=0x0(none) hashes=300+7 location=embedded
CDHash=b7dd1973881be3cadcba4d2df388aafd87ea8994
Authority=Apple Mac OS Application Signing
Authority=Apple Worldwide Developer Relations Certification Authority
Authority=Apple Root CA
TeamIdentifier=74J34U3R6X
`
	codesignAppleSystem = `Executable=/System/Applications/Calculator.app/Contents/MacOS/Calculator
CodeDirectory v=20400 size=3757 flags=0x0(none) hashes=107+7 location=embedded
Platform identifier=26
CDHash=0d31d7be553350e28099af7cb096f002ef16c993
Authority=macOS Software Signing
Authority=Apple Code Signing Certification Authority
Authority=Apple Root CA
TeamIdentifier=not set
`
	codesignAdHoc = `Executable=/tmp/adhoc
CodeDirectory v=20400 size=254 flags=0x20002(adhoc,linker-signed) hashes=5+0 location=embedded
CDHash=9b84d9ab51a6ef7b08f5b66a386f72ef06f1a487
Signature=adhoc
Info.plist=not bound
TeamIdentifier=not set
`
	codesignUnsigned = "/tmp/Unsigned.app: code object is not signed at all\n"
)

func TestInferGatekeeper(t *testing.T) {
	cases := []struct {
		name, out, likely, source, cdHash string
	}{
		{"developer id stapled", codesignDevIDStapled, "accepted", "Notarized Developer ID", "595aa36a53e9a1c89a72d52981b7eb9e0ccb70ea"},
		{"developer id unstapled", codesignDevIDUnstapled, "", "", "050ea16c60f05ed9d31ed1ab8c8f70ba55ad2e70"},
		{"mac app store", codesignMAS, "accepted", "Mac App Store", "b7dd1973881be3cadcba4d2df388aafd87ea8994"},
		{"apple system", codesignAppleSystem, "accepted", "Apple System", "0d31d7be553350e28099af7cb096f002ef16c993"},
		{"ad-hoc", codesignAdHoc, "", "", "9b84d9ab51a6ef7b08f5b66a386f72ef06f1a487"},
		{"unsigned", codesignUnsigned, "", "", ""},
		{"empty", "", "", "", ""},
		{"non-apple root", "Authority=Developer ID Application: X (ABC)\nAuthority=Developer ID Certification Authority\nAuthority=Evil Root\nNotarization Ticket=stapled\n", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			si := parseSigningInfo(tc.out)
			likely, source := inferGatekeeper(si)
			if likely != tc.likely || source != tc.source {
				t.Errorf("inferGatekeeper = %q/%q, want %q/%q", likely, source, tc.likely, tc.source)
			}
			if likely == "rejected" {
				t.Errorf("inference must never predict rejected")
			}
			if si.cdHash != tc.cdHash {
				t.Errorf("cdHash = %q, want %q", si.cdHash, tc.cdHash)
			}
		})
	}
}

func TestParseSigningInfoKeepsLegacyFields(t *testing.T) {
	team, hardened := ParseSigningOutput(codesignDevIDStapled)
	if team != "EQHXZ8M8AV" || !hardened {
		t.Fatalf("ParseSigningOutput = %q/%v", team, hardened)
	}
	si := parseSigningInfo(codesignDevIDStapled)
	if !si.ticketStapled || len(si.authorities) != 3 {
		t.Fatalf("parseSigningInfo = %+v", si)
	}
	if !parseSigningInfo(codesignAdHoc).adHoc || !parseSigningInfo(codesignUnsigned).unsigned {
		t.Fatal("ad-hoc / unsigned flags not parsed")
	}
}

func newTestAssessor(now func() time.Time) *GatekeeperAssessor {
	return NewGatekeeperAssessor(time.Second, time.Hour, now)
}

// blockingSpctl never answers until its context ends, like spctl stuck on
// syspolicyd.
type blockingSpctl struct{ calls atomic.Int32 }

func (f *blockingSpctl) Run(ctx context.Context, _ proc.Cmd) (proc.Result, error) {
	f.calls.Add(1)
	<-ctx.Done()
	return proc.Result{ExitCode: -1}, ctx.Err()
}

func TestAssessTimeoutIsNotCached(t *testing.T) {
	a := NewGatekeeperAssessor(10*time.Millisecond, time.Hour, nil)
	f := &blockingSpctl{}
	for i := 0; i < 2; i++ {
		v := a.assess(context.Background(), f, "/Applications/Slow.app", "cdhash1")
		if v.status != "" || v.err != GatekeeperErrorTimeout {
			t.Fatalf("assess = %+v, want timeout with no status", v)
		}
	}
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("spctl ran %d times, want 2 (timeout must not be cached)", got)
	}
}

// countingSpctl answers accepted and records peak concurrency.
type countingSpctl struct {
	calls   atomic.Int32
	active  atomic.Int32
	peak    atomic.Int32
	release chan struct{}
}

func (f *countingSpctl) Run(_ context.Context, cmd proc.Cmd) (proc.Result, error) {
	f.calls.Add(1)
	n := f.active.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if f.release != nil {
		<-f.release
	}
	f.active.Add(-1)
	app := cmd.Args[len(cmd.Args)-1]
	return proc.Result{Stderr: []byte(app + ": accepted\nsource=Notarized Developer ID\n")}, nil
}

func TestAssessSerializesSpctl(t *testing.T) {
	a := newTestAssessor(nil)
	f := &countingSpctl{release: make(chan struct{})}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct CDHashes so no call is served from the cache.
			a.assess(context.Background(), f, "/Applications/App.app", string(rune('a'+i)))
		}(i)
	}
	for i := 0; i < 4; i++ {
		f.release <- struct{}{}
	}
	wg.Wait()
	if f.peak.Load() != 1 || f.calls.Load() != 4 {
		t.Fatalf("peak concurrency %d over %d calls, want 1 over 4", f.peak.Load(), f.calls.Load())
	}
}

func TestAssessCachesVerdictByCDHash(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	a := NewGatekeeperAssessor(time.Second, 24*time.Hour, func() time.Time { return now })
	f := &countingSpctl{}
	a.assess(context.Background(), f, "/Applications/App.app", "hash")
	now = now.Add(23 * time.Hour)
	if v := a.assess(context.Background(), f, "/Applications/App.app", "hash"); v.status != "accepted" || f.calls.Load() != 1 {
		t.Fatalf("within TTL: %+v after %d calls, want cached accepted after 1", v, f.calls.Load())
	}
	a.assess(context.Background(), f, "/Applications/App.app", "other-hash")
	if f.calls.Load() != 2 {
		t.Fatalf("new CDHash should miss, calls=%d", f.calls.Load())
	}
	now = now.Add(2 * time.Hour)
	a.assess(context.Background(), f, "/Applications/App.app", "hash")
	if f.calls.Load() != 3 {
		t.Fatalf("expired entry should re-run spctl, calls=%d", f.calls.Load())
	}
	a.assess(context.Background(), f, "/Applications/App.app", "")
	a.assess(context.Background(), f, "/Applications/App.app", "")
	if f.calls.Load() != 5 {
		t.Fatalf("empty CDHash must not be cached, calls=%d", f.calls.Load())
	}
}

func TestAssessNoVerdictReason(t *testing.T) {
	r := proc.NewFake()
	r.On(proc.MatchPrefix("spctl"), proc.Response{Result: proc.Result{ExitCode: 3, Stderr: []byte("/Applications/M.app: a sealed resource is missing or invalid\n")}})
	v := newTestAssessor(nil).assess(context.Background(), r, "/Applications/M.app", "h")
	if v.status != "" || v.err != "no verdict: a sealed resource is missing or invalid" {
		t.Fatalf("assess = %+v", v)
	}
}

// signingFake wraps toolFake, answering codesign -dv with a fixture and
// spctl through an optional inner runner.
type signingFake struct {
	*toolFake
	codesign string
	spctl    proc.Runner
	verify   proc.Runner // codesign --verify; nil answers "valid"
}

func (f signingFake) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	switch {
	case cmd.Name == "codesign" && len(cmd.Args) > 0 && cmd.Args[0] == "-dv":
		return proc.Result{ExitCode: 0, Stderr: []byte(f.codesign)}, nil
	case cmd.Name == "codesign" && len(cmd.Args) > 0 && cmd.Args[0] == "--verify":
		if f.verify != nil {
			return f.verify.Run(ctx, cmd)
		}
		return proc.Result{}, nil
	case cmd.Name == "spctl" && f.spctl != nil:
		return f.spctl.Run(ctx, cmd)
	}
	return f.toolFake.Run(ctx, cmd)
}

func TestDetectWithInfersGatekeeperWithoutSpctl(t *testing.T) {
	app := makeBundle(t, "Chrome")
	spctl := &countingSpctl{}
	f := signingFake{toolFake: newToolFake().bundlePlist(t, app, nil), codesign: codesignDevIDStapled, spctl: spctl}
	r, err := detectDarwinRunner(t, app, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if spctl.calls.Load() != 0 {
		t.Fatalf("spctl ran %d times without AssessGatekeeper", spctl.calls.Load())
	}
	if r.GatekeeperStatus != "" || r.GatekeeperSource != "" {
		t.Fatalf("inference leaked into the assessed verdict: %q/%q", r.GatekeeperStatus, r.GatekeeperSource)
	}
	if r.GatekeeperLikely != "accepted" || r.GatekeeperLikelySource != "Notarized Developer ID" || r.GatekeeperMethod != GatekeeperMethodSignature {
		t.Fatalf("likely = %q/%q method %q", r.GatekeeperLikely, r.GatekeeperLikelySource, r.GatekeeperMethod)
	}
	if r.CDHash != "595aa36a53e9a1c89a72d52981b7eb9e0ccb70ea" {
		t.Fatalf("CDHash = %q", r.CDHash)
	}
}

func TestDetectWithAssessGatekeeper(t *testing.T) {
	app := makeBundle(t, "Chrome")
	spctl := &countingSpctl{}
	f := signingFake{toolFake: newToolFake().bundlePlist(t, app, nil), codesign: codesignDevIDStapled, spctl: spctl}
	r, err := detectDarwinRunner(t, app, f, Options{AssessGatekeeper: true, Gatekeeper: newTestAssessor(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if r.GatekeeperStatus != "accepted" || r.GatekeeperMethod != GatekeeperMethodSpctl || r.GatekeeperError != "" {
		t.Fatalf("assessed = %q method %q err %q", r.GatekeeperStatus, r.GatekeeperMethod, r.GatekeeperError)
	}

	timedOut := signingFake{toolFake: f.toolFake, codesign: codesignDevIDStapled, spctl: &blockingSpctl{}}
	r, err = detectDarwinRunner(t, app, timedOut, Options{AssessGatekeeper: true, Gatekeeper: NewGatekeeperAssessor(10*time.Millisecond, time.Hour, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if r.GatekeeperStatus != "" || r.GatekeeperError != GatekeeperErrorTimeout || r.GatekeeperMethod != GatekeeperMethodSignature {
		t.Fatalf("timeout = %q err %q method %q", r.GatekeeperStatus, r.GatekeeperError, r.GatekeeperMethod)
	}
	stripped := WithoutGatekeeperAssessment(r)
	if stripped.GatekeeperError != "" || stripped.GatekeeperLikely != "accepted" {
		t.Fatalf("WithoutGatekeeperAssessment = %+v", stripped)
	}
}

func detectDarwinRunner(tb testing.TB, app string, r proc.Runner, opts Options) (Result, error) {
	tb.Helper()
	opts.Runner = r
	opts.OS = hostos.Darwin
	return DetectWith(app, opts)
}

func TestAssessCacheKeyedByPathAndCDHash(t *testing.T) {
	a := newTestAssessor(nil)
	f := &countingSpctl{}
	a.assess(context.Background(), f, "/Applications/Foo.app", "hash")
	a.assess(context.Background(), f, "/Applications/Foo 2.app", "hash")
	if f.calls.Load() != 2 {
		t.Fatalf("an identical copy at another path reused the verdict (calls=%d)", f.calls.Load())
	}
}

func TestAssessCanceledWhileQueued(t *testing.T) {
	a := newTestAssessor(nil)
	hold := &countingSpctl{release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		a.assess(context.Background(), hold, "/Applications/First.app", "h1")
		close(done)
	}()
	for hold.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queued := &countingSpctl{}
	v := a.assess(ctx, queued, "/Applications/Second.app", "h2")
	if v.err != GatekeeperErrorCanceled || queued.calls.Load() != 0 {
		t.Fatalf("queued+canceled = %+v after %d spctl runs, want canceled without running", v, queued.calls.Load())
	}
	hold.release <- struct{}{}
	<-done
	if _, ok := a.cached(verdictKey("/Applications/Second.app", "h2")); ok {
		t.Fatal("cancellation was cached")
	}
}

func TestAssessCanceledWhileRunning(t *testing.T) {
	a := NewGatekeeperAssessor(time.Hour, time.Hour, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if v := a.assess(ctx, &blockingSpctl{}, "/Applications/Slow.app", "h"); v.err != GatekeeperErrorCanceled {
		t.Fatalf("assess = %+v, want canceled", v)
	}
}

// memSealCache is an in-memory SealCache.
type memSealCache struct {
	mu   sync.Mutex
	m    map[string][]byte
	puts int
}

func newMemSealCache() *memSealCache { return &memSealCache{m: map[string][]byte{}} }

func (c *memSealCache) Get(key []byte) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[string(key)]
	return b, ok
}

func (c *memSealCache) Put(key, payload []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[string(key)] = payload
	c.puts++
	return nil
}

func sealRunner(exit int, stderr string, err error) *proc.Fake {
	return proc.NewFake().On(proc.MatchPrefix("codesign", "--verify"),
		proc.Response{Result: proc.Result{ExitCode: exit, Stderr: []byte(stderr)}, Err: err})
}

func TestLikelyGatekeeperSealCheck(t *testing.T) {
	const app = "/Applications/Figma.app"
	si := parseSigningInfo(codesignDevIDStapled)
	cases := []struct {
		name     string
		verify   *proc.Fake
		want     gatekeeperLikely
		cachedOK bool
	}{
		{"seal ok", sealRunner(0, "", nil), gatekeeperLikely{likely: "accepted", source: "Notarized Developer ID"}, true},
		{"seal broken", sealRunner(1, app+": a sealed resource is missing or invalid\n", nil), gatekeeperLikely{reason: GatekeeperReasonSealInvalid}, true},
		{"only detritus", sealRunner(1, app+": resource fork, Finder information, or similar detritus not allowed\n", nil), gatekeeperLikely{likely: "accepted", source: "Notarized Developer ID"}, true},
		{"other failure", sealRunner(1, app+": invalid signature (code or signature have been modified)\n", nil), gatekeeperLikely{reason: "signature invalid: invalid signature (code or signature have been modified)"}, true},
		{"timeout", nil, gatekeeperLikely{reason: GatekeeperReasonSealUnchecked + ": timeout"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemSealCache()
			opts := Options{SealCache: store, SealTimeout: 10 * time.Millisecond}
			if tc.verify != nil {
				opts.Runner = tc.verify
			} else {
				opts.Runner = &blockingSpctl{}
			}
			if got := newDetector(opts).likelyGatekeeper(app, si); got != tc.want {
				t.Fatalf("likelyGatekeeper = %+v, want %+v", got, tc.want)
			}
			if (store.puts == 1) != tc.cachedOK {
				t.Fatalf("seal outcome cached %d times, want cached=%v", store.puts, tc.cachedOK)
			}
		})
	}
}

func TestSealCacheHitSkipsVerify(t *testing.T) {
	const app = "/Applications/Xcode.app"
	si := parseSigningInfo(codesignMAS)
	store := newMemSealCache()
	first := sealRunner(0, "", nil)
	newDetector(Options{SealCache: store, Runner: first}).likelyGatekeeper(app, si)
	second := proc.NewFake() // any codesign call would fail the prediction
	got := newDetector(Options{SealCache: store, Runner: second}).likelyGatekeeper(app, si)
	if got.likely != "accepted" || second.CallCount() != 0 || first.CallCount() != 1 {
		t.Fatalf("cache hit = %+v, verify calls first=%d second=%d", got, first.CallCount(), second.CallCount())
	}
	// Another path or CDHash is a different entry.
	newDetector(Options{SealCache: store, Runner: second}).likelyGatekeeper("/Applications/Xcode 2.app", si)
	if second.CallCount() != 1 {
		t.Fatalf("copy at another path hit the cache (calls=%d)", second.CallCount())
	}
}

func TestSealCheckOnlyForLikelyAccepted(t *testing.T) {
	r := proc.NewFake()
	for _, out := range []string{codesignDevIDUnstapled, codesignAdHoc, codesignUnsigned} {
		if got := newDetector(Options{Runner: r}).likelyGatekeeper("/Applications/X.app", parseSigningInfo(out)); got != (gatekeeperLikely{}) {
			t.Errorf("likelyGatekeeper = %+v, want empty", got)
		}
	}
	if r.CallCount() != 0 {
		t.Fatalf("seal check ran %d times for bundles that would not be inferred accepted", r.CallCount())
	}
}

func TestDetectWithBrokenSeal(t *testing.T) {
	app := makeBundle(t, "Figma")
	f := signingFake{
		toolFake: newToolFake().bundlePlist(t, app, nil),
		codesign: codesignDevIDStapled,
		verify:   sealRunner(1, app+": a sealed resource is missing or invalid\n", nil),
	}
	r, err := detectDarwinRunner(t, app, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r.GatekeeperLikely != "" || r.GatekeeperLikelyReason != GatekeeperReasonSealInvalid || r.GatekeeperMethod != GatekeeperMethodSignature || r.GatekeeperStatus != "" {
		t.Fatalf("broken seal = likely %q reason %q method %q status %q", r.GatekeeperLikely, r.GatekeeperLikelyReason, r.GatekeeperMethod, r.GatekeeperStatus)
	}
}

func TestRefreshSignatureUsesCallerPath(t *testing.T) {
	app := makeBundle(t, "Copy")
	f := signingFake{toolFake: newToolFake(), codesign: codesignMAS}
	r := Result{Path: "/Applications/Original.app", CDHash: "stale", GatekeeperLikely: "accepted"}
	RefreshSignature(&r, app, Options{Runner: f})
	if r.Path != app || r.CDHash != "b7dd1973881be3cadcba4d2df388aafd87ea8994" || r.GatekeeperLikelySource != "Mac App Store" {
		t.Fatalf("RefreshSignature = path %q cdhash %q source %q", r.Path, r.CDHash, r.GatekeeperLikelySource)
	}
}
