package detect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/codesignout"
	"github.com/kaeawc/spectra/internal/proc"
)

// Gatekeeper evidence comes from two places with very different costs.
//
// The signature (`codesign -dv --verbose=4`, ~30 ms) names the authority
// chain and whether a notarization ticket is stapled, which is enough to
// predict the verdict for the common distribution channels once a plain
// `codesign --verify` confirms the resource seal (cached on disk per
// path+CDHash, since it hashes every resource). `spctl --assess` is the
// real verdict, but syspolicyd serializes assessments and can spend minutes
// on per-nested-binary notarization lookups (Chrome, Xcode), so it only
// runs when a caller opts in, one at a time, under its own short timeout.

// Gatekeeper method values recorded in Result.GatekeeperMethod.
const (
	GatekeeperMethodSpctl     = "spctl"
	GatekeeperMethodSignature = "signature"
)

// Result.GatekeeperError values for a requested assessment with no verdict.
const (
	GatekeeperErrorTimeout  = "timeout"
	GatekeeperErrorCanceled = "canceled"
)

// Result.GatekeeperLikelyReason values. A reason beginning with
// GatekeeperReasonSealUnchecked means the prediction is unknown, not
// negative.
const (
	GatekeeperReasonSealInvalid   = "resource seal invalid"
	GatekeeperReasonSealUnchecked = "seal not checked"
)

const (
	defaultGatekeeperTimeout = 15 * time.Second
	defaultGatekeeperTTL     = 24 * time.Hour
	defaultSealTimeout       = 30 * time.Second
	sealCacheTTL             = 30 * 24 * time.Hour
)

// SealCache persists `codesign --verify` outcomes across runs.
// *cache.TTLStore satisfies it.
type SealCache interface {
	Get(key []byte) ([]byte, bool)
	Put(key, payload []byte, ttl time.Duration) error
}

// signingInfo is what `codesign -dv --verbose=4` reports about a bundle.
type signingInfo struct {
	teamID        string
	hardened      bool
	authorities   []string // leaf first, root last
	ticketStapled bool
	cdHash        string
	adHoc         bool
	unsigned      bool
}

func parseSigningInfo(out string) signingInfo {
	var si signingInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "TeamIdentifier="):
			if v := strings.TrimPrefix(line, "TeamIdentifier="); v != "not set" {
				si.teamID = v
			}
		case strings.HasPrefix(line, "Authority="):
			si.authorities = append(si.authorities, strings.TrimPrefix(line, "Authority="))
		case strings.HasPrefix(line, "CDHash="):
			si.cdHash = strings.TrimPrefix(line, "CDHash=")
		case line == "Notarization Ticket=stapled":
			si.ticketStapled = true
		case line == "Signature=adhoc":
			si.adHoc = true
		case strings.HasSuffix(line, "code object is not signed at all"):
			si.unsigned = true
		}
		if hasCodeDirectoryFlag(line, "runtime") {
			si.hardened = true
		}
	}
	return si
}

// inferGatekeeper predicts the spctl verdict from the signature alone. It
// only ever predicts "accepted", for chains whose acceptance does not hinge
// on an online lookup; anything else (unsigned, ad-hoc, unknown chains,
// Developer ID without a stapled ticket) yields "" rather than a guess, so
// the inference can never stand in for a rejection.
func inferGatekeeper(si signingInfo) (likely, source string) {
	if si.unsigned || si.adHoc || len(si.authorities) < 2 {
		return "", ""
	}
	if si.authorities[len(si.authorities)-1] != "Apple Root CA" {
		return "", ""
	}
	leaf, issuer := si.authorities[0], si.authorities[1]
	switch {
	case strings.HasPrefix(leaf, "Developer ID Application:") &&
		issuer == "Developer ID Certification Authority" && si.ticketStapled:
		return "accepted", "Notarized Developer ID"
	case leaf == "Apple Mac OS Application Signing" &&
		issuer == "Apple Worldwide Developer Relations Certification Authority":
		return "accepted", "Mac App Store"
	case (leaf == "Software Signing" || leaf == "macOS Software Signing") &&
		issuer == "Apple Code Signing Certification Authority":
		return "accepted", "Apple System"
	}
	return "", ""
}

// gatekeeperLikely is the signature-based prediction plus, when the chain
// alone would have predicted acceptance but the seal check disagreed or
// could not run, why it is withheld.
type gatekeeperLikely struct {
	likely, source, reason string
}

// likelyGatekeeper confirms an "accepted" inference with a resource-seal
// check. Only bundles that would otherwise be inferred accepted pay for it.
func (d detector) likelyGatekeeper(appPath string, si signingInfo) gatekeeperLikely {
	likely, source := inferGatekeeper(si)
	if likely == "" {
		return gatekeeperLikely{}
	}
	if seal := d.verifySeal(appPath, si.cdHash); !seal.OK {
		return gatekeeperLikely{reason: seal.Reason}
	}
	return gatekeeperLikely{likely: likely, source: source}
}

// sealOutcome is one `codesign --verify` result, as persisted.
type sealOutcome struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

var sealCacheSchema = []byte("seal-v1\x00")

func sealCacheKey(appPath, cdHash string) []byte {
	return cache.Key(sealCacheSchema, []byte(appPath), []byte{0}, []byte(cdHash))
}

// verifySeal returns the cached seal outcome for appPath at cdHash, running
// `codesign --verify` on a miss. Inconclusive runs (timeout, cancellation,
// codesign missing) are not cached.
func (d detector) verifySeal(appPath, cdHash string) sealOutcome {
	store := d.opts.SealCache
	cacheable := store != nil && cdHash != ""
	key := sealCacheKey(appPath, cdHash)
	if cacheable {
		if blob, ok := store.Get(key); ok {
			var o sealOutcome
			if json.Unmarshal(blob, &o) == nil {
				return o
			}
		}
	}
	o, conclusive := d.runSealVerify(appPath)
	if cacheable && conclusive {
		if blob, err := json.Marshal(o); err == nil {
			_ = store.Put(key, blob, sealCacheTTL)
		}
	}
	return o
}

func (d detector) runSealVerify(appPath string) (sealOutcome, bool) {
	timeout := d.opts.SealTimeout
	if timeout <= 0 {
		timeout = defaultSealTimeout
	}
	parent := d.context()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	runner := d.opts.Runner
	if runner == nil {
		runner = proc.Default
	}
	// No --strict: Finder metadata does not stop Gatekeeper (Chrome), and
	// no --deep: nested code is spctl's business, not this prediction's.
	res, err := runner.Run(ctx, proc.Cmd{Name: "codesign", Args: []string{"--verify", appPath}})
	switch {
	case parent.Err() != nil:
		return sealOutcome{Reason: GatekeeperReasonSealUnchecked + ": canceled"}, false
	case ctx.Err() != nil:
		return sealOutcome{Reason: GatekeeperReasonSealUnchecked + ": timeout"}, false
	case err != nil:
		return sealOutcome{Reason: GatekeeperReasonSealUnchecked + ": codesign unavailable"}, false
	case res.ExitCode == 0:
		return sealOutcome{OK: true}, true
	}
	out := string(append(res.Stdout, res.Stderr...))
	if codesignout.OnlyDetritus(out) {
		return sealOutcome{OK: true}, true
	}
	if strings.Contains(out, "a sealed resource is missing or invalid") {
		return sealOutcome{Reason: GatekeeperReasonSealInvalid}, true
	}
	reason := "signature invalid"
	if msg := toolMessage(out, appPath); msg != "" {
		reason += ": " + msg
	}
	return sealOutcome{Reason: reason}, true
}

// GatekeeperAssessor runs bounded, serialized `spctl --assess` calls and
// remembers real verdicts by bundle path and main-executable CDHash. It is
// safe for concurrent use; concurrent callers queue behind one another
// because syspolicyd serializes assessments anyway and parallel requests
// only make each other time out.
type GatekeeperAssessor struct {
	timeout time.Duration
	ttl     time.Duration
	now     func() time.Time
	sem     chan struct{}

	mu    sync.Mutex
	cache map[string]gatekeeperCacheEntry
}

type gatekeeperCacheEntry struct {
	verdict gatekeeperVerdict
	at      time.Time
}

// NewGatekeeperAssessor returns an assessor whose spctl runs are bounded
// by timeout and whose verdicts are reused for ttl. A nil now uses the
// wall clock.
func NewGatekeeperAssessor(timeout, ttl time.Duration, now func() time.Time) *GatekeeperAssessor {
	if now == nil {
		now = time.Now
	}
	return &GatekeeperAssessor{
		timeout: timeout,
		ttl:     ttl,
		now:     now,
		sem:     make(chan struct{}, 1),
		cache:   map[string]gatekeeperCacheEntry{},
	}
}

var defaultGatekeeperAssessor = NewGatekeeperAssessor(defaultGatekeeperTimeout, defaultGatekeeperTTL, nil)

func verdictKey(appPath, cdHash string) string {
	if cdHash == "" {
		return ""
	}
	return appPath + "\x00" + cdHash
}

func (a *GatekeeperAssessor) cached(key string) (gatekeeperVerdict, bool) {
	if key == "" {
		return gatekeeperVerdict{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.cache[key]
	if !ok || a.now().Sub(e.at) >= a.ttl {
		return gatekeeperVerdict{}, false
	}
	return e.verdict, true
}

func (a *GatekeeperAssessor) store(key string, v gatekeeperVerdict) {
	if key == "" || v.status == "" {
		return
	}
	a.mu.Lock()
	a.cache[key] = gatekeeperCacheEntry{verdict: v, at: a.now()}
	a.mu.Unlock()
}

// assess returns the spctl verdict for appPath. Only real verdicts are
// cached; a timeout, cancellation, or unassessable bundle is retried next
// time. A caller canceled while queued gives up without running spctl.
func (a *GatekeeperAssessor) assess(ctx context.Context, runner proc.Runner, appPath, cdHash string) gatekeeperVerdict {
	key := verdictKey(appPath, cdHash)
	if v, ok := a.cached(key); ok {
		return v
	}
	select {
	case a.sem <- struct{}{}:
	case <-ctx.Done():
		return gatekeeperVerdict{err: GatekeeperErrorCanceled}
	}
	defer func() { <-a.sem }()
	// Another caller may have assessed the same binary while we queued.
	if v, ok := a.cached(key); ok {
		return v
	}
	v := a.run(ctx, runner, appPath)
	a.store(key, v)
	return v
}

func (a *GatekeeperAssessor) run(parent context.Context, runner proc.Runner, appPath string) gatekeeperVerdict {
	if runner == nil {
		runner = proc.Default
	}
	ctx, cancel := context.WithTimeout(parent, a.timeout)
	defer cancel()
	res, err := runner.Run(ctx, proc.Cmd{Name: "spctl", Args: []string{"--assess", "-v", "--type", "exec", appPath}})
	switch {
	case parent.Err() != nil:
		return gatekeeperVerdict{err: GatekeeperErrorCanceled}
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return gatekeeperVerdict{err: GatekeeperErrorTimeout}
	case err != nil:
		return gatekeeperVerdict{err: "spctl unavailable"}
	}
	out := string(append(res.Stdout, res.Stderr...))
	v := parseGatekeeperOutput(out)
	if v.status == "" {
		v.err = "no verdict"
		if msg := toolMessage(out, appPath); msg != "" {
			v.err += ": " + msg
		}
	}
	return v
}

// toolMessage returns the reason spctl or codesign printed for appPath
// ("a sealed resource is missing or invalid"), without the path.
func toolMessage(out, appPath string) string {
	for _, line := range strings.Split(out, "\n") {
		if msg, ok := strings.CutPrefix(strings.TrimSpace(line), appPath+": "); ok {
			return msg
		}
	}
	return ""
}

func (d detector) gatekeeperAssessor() *GatekeeperAssessor {
	if d.opts.Gatekeeper != nil {
		return d.opts.Gatekeeper
	}
	return defaultGatekeeperAssessor
}

func (d detector) context() context.Context {
	if d.opts.Context != nil {
		return d.opts.Context
	}
	return context.Background()
}

func (d detector) assessGatekeeper(appPath, cdHash string) gatekeeperVerdict {
	return d.gatekeeperAssessor().assess(d.context(), d.opts.Runner, appPath, cdHash)
}

// RefreshSignature re-derives the signature-dependent fields of r for the
// bundle at appPath — Path, TeamID, HardenedRuntime, CDHash, and the
// Gatekeeper prediction — and, when opts.AssessGatekeeper is set, runs the
// spctl assessment. Callers serving r from a content-keyed cache use it so
// an identical copy at another path, or a re-signed bundle, is never
// judged by the cached entry's signature. It is one `codesign -dv` plus a
// seal check that opts.SealCache usually answers.
func RefreshSignature(r *Result, appPath string, opts Options) {
	d := newDetector(opts)
	r.Path = appPath
	si := d.readSigning(appPath)
	r.TeamID, r.HardenedRuntime, r.CDHash = si.teamID, si.hardened, si.cdHash
	applyGatekeeperLikely(r, d.likelyGatekeeper(appPath, si))
	var v gatekeeperVerdict
	if opts.AssessGatekeeper {
		v = d.assessGatekeeper(appPath, r.CDHash)
	}
	applyGatekeeperVerdict(r, v)
}

// WithoutGatekeeperAssessment returns r with the spctl verdict removed,
// keeping the signature inference. Persistent caches store this form so a
// verdict outlives neither its TTL nor a timeout.
func WithoutGatekeeperAssessment(r Result) Result {
	applyGatekeeperVerdict(&r, gatekeeperVerdict{})
	return r
}

func applyGatekeeperLikely(r *Result, l gatekeeperLikely) {
	r.GatekeeperLikely, r.GatekeeperLikelySource, r.GatekeeperLikelyReason = l.likely, l.source, l.reason
}

func applyGatekeeperVerdict(r *Result, v gatekeeperVerdict) {
	r.GatekeeperStatus = v.status
	r.GatekeeperSource = v.source
	r.GatekeeperError = v.err
	switch {
	case v.status != "":
		r.GatekeeperMethod = GatekeeperMethodSpctl
	case r.GatekeeperLikely != "" || r.GatekeeperLikelyReason != "":
		r.GatekeeperMethod = GatekeeperMethodSignature
	default:
		r.GatekeeperMethod = ""
	}
}
