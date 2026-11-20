package mcp

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// defaultLivePollInterval paces local host sampling and daemon reconnect
	// attempts while no daemon stream is available.
	defaultLivePollInterval = 30 * time.Second
	// defaultHealthMinInterval is the shortest gap between two host-health
	// updated notifications.
	defaultHealthMinInterval = 10 * time.Second
	// loadHoldPerCPU matches the `load` tool's default max_load_per_cpu; the
	// verdict returns to admit below loadReleasePerCPU so load hovering at
	// the limit does not flap.
	loadHoldPerCPU     = 1.5
	loadReleasePerCPU  = 1.35
	localSampleTimeout = 15 * time.Second
	// defaultAlertDebounce collapses a burst of alert events into one
	// firing-alerts update.
	defaultAlertDebounce = time.Second
)

// daemonStreamClient is the part of the daemon client the live watcher uses.
// *watchclient.Client implements it.
type daemonStreamClient interface {
	Subscribe(ctx context.Context, samples bool, onEvent func(hostwatch.Event), onSample func(hostwatch.Sample)) error
	Close() error
}

type liveConfig struct {
	pollInterval      time.Duration
	healthMinInterval time.Duration
	sampleLocal       func(context.Context) (hostwatch.Sample, error)
	alertDebounce     time.Duration
	// afterFunc schedules f after d and returns its stop function; tests
	// replace it to drive the debounce without waiting.
	afterFunc func(d time.Duration, f func()) (stop func() bool)
}

func (c liveConfig) withDefaults() liveConfig {
	if c.pollInterval <= 0 {
		c.pollInterval = defaultLivePollInterval
	}
	if c.healthMinInterval <= 0 {
		c.healthMinInterval = defaultHealthMinInterval
	}
	if c.alertDebounce <= 0 {
		c.alertDebounce = defaultAlertDebounce
	}
	if c.afterFunc == nil {
		c.afterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if c.sampleLocal == nil {
		collector := &hostwatch.Collector{}
		c.sampleLocal = collector.Collect
	}
	return c
}

// healthLevel is what a host-health notification tracks. Raw load and free
// memory move every sample; only a change here is worth telling a client.
type healthLevel struct {
	daemon         bool
	loadVerdict    string
	memoryPressure string
	thermal        bool
}

// liveHub turns daemon alert events and host samples into resources/updated
// notifications. One watcher goroutine serves every session; it runs only
// while at least one session subscribes to a live resource.
type liveHub struct {
	cfg     liveConfig
	connect func(context.Context) (daemonStreamClient, bool)
	notify  func(uri string)
	now     func() time.Time

	mu       sync.Mutex
	closed   bool
	sessions map[*sdk.ServerSession]map[string]bool
	counts   map[string]int
	gen      uint64
	cancel   context.CancelFunc
	done     chan struct{}

	daemonKnown      bool
	daemonUp         bool
	level            healthLevel // hysteresis state, advanced on every sample
	notified         healthLevel // the level last sent in a notification
	haveLevel        bool
	lastHealthNotify time.Time
	localSample      hostwatch.Sample
	localAt          time.Time
	alertStop        func() bool // pending debounced alerts update
	streamErr        string      // why the daemon's stream is unusable, if it is
}

var liveURIs = map[string]bool{firingAlertsURI: true, hostHealthURI: true}

func newLiveHub(cfg liveConfig, connect func(context.Context) (daemonStreamClient, bool), now func() time.Time) *liveHub {
	return &liveHub{
		cfg:      cfg.withDefaults(),
		connect:  connect,
		notify:   func(string) {},
		now:      now,
		sessions: map[*sdk.ServerSession]map[string]bool{},
		counts:   map[string]int{},
	}
}

// streamConnector adapts the server's daemon connector to the stream client
// the watcher needs. Each call opens a dedicated connection: the stream owns
// its client's notification handler, so it cannot share the request client.
func streamConnector(connect func(context.Context) (daemonWatchClient, bool)) func(context.Context) (daemonStreamClient, bool) {
	if connect == nil {
		connect = defaultDaemonConnect
	}
	return func(ctx context.Context) (daemonStreamClient, bool) {
		c, ok := connect(ctx)
		if !ok {
			return nil, false
		}
		sc, ok := c.(daemonStreamClient)
		if !ok {
			_ = c.Close()
			return nil, false
		}
		return sc, true
	}
}

func (h *liveHub) subscribe(_ context.Context, req *sdk.SubscribeRequest) error {
	uri := req.Params.URI
	if !knownResource(uri) {
		return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown resource %q", uri)}
	}
	// Static resources never change; accept the subscription and send nothing.
	if liveURIs[uri] && req.Session != nil {
		h.add(req.Session, uri)
	}
	return nil
}

func (h *liveHub) unsubscribe(_ context.Context, req *sdk.UnsubscribeRequest) error {
	if req.Session != nil {
		h.remove(req.Session, req.Params.URI)
	}
	return nil
}

func knownResource(uri string) bool {
	for _, r := range resourceDefinitions() {
		if r.URI == uri {
			return true
		}
	}
	return false
}

func (h *liveHub) add(ss *sdk.ServerSession, uri string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	set := h.sessions[ss]
	if set == nil {
		set = map[string]bool{}
		h.sessions[ss] = set
		// The SDK forgets a closed session's subscriptions without calling
		// the unsubscribe handler, so drop them here when it ends.
		go func() {
			_ = ss.Wait()
			h.dropSession(ss)
		}()
	}
	if set[uri] {
		return
	}
	set[uri] = true
	h.counts[uri]++
	h.startLocked()
}

func (h *liveHub) remove(ss *sdk.ServerSession, uri string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.sessions[ss]
	if !set[uri] {
		return
	}
	delete(set, uri)
	h.decLocked(uri)
	h.stopIfIdleLocked()
}

func (h *liveHub) dropSession(ss *sdk.ServerSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for uri := range h.sessions[ss] {
		h.decLocked(uri)
	}
	delete(h.sessions, ss)
	h.stopIfIdleLocked()
}

func (h *liveHub) decLocked(uri string) {
	if h.counts[uri]--; h.counts[uri] <= 0 {
		delete(h.counts, uri)
		if uri == hostHealthURI {
			// No sample updates the level without a host-health subscriber.
			h.haveLevel = false
		}
	}
}

func (h *liveHub) startLocked() {
	if h.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	prev := h.done
	h.gen++
	h.cancel = cancel
	h.done = make(chan struct{})
	h.daemonKnown, h.haveLevel = false, false
	h.lastHealthNotify = time.Time{}
	h.streamErr = ""
	go h.run(ctx, h.gen, prev, h.done)
}

func (h *liveHub) stopIfIdleLocked() {
	if len(h.counts) > 0 || h.cancel == nil {
		return
	}
	h.cancel()
	h.cancel = nil
	h.retireLocked()
	// A later start waits for this watcher to exit, so at most one runs.
}

// retireLocked forgets the stopped watcher's state and moves to a new
// generation, so late updates from the cancelled watcher are ignored.
func (h *liveHub) retireLocked() {
	h.gen++
	h.streamErr = ""
	h.stopAlertTimerLocked()
}

func (h *liveHub) stopAlertTimerLocked() {
	if h.alertStop != nil {
		h.alertStop()
		h.alertStop = nil
	}
}

// close stops the watcher and waits for it; later subscriptions are ignored.
func (h *liveHub) close() {
	h.mu.Lock()
	h.closed = true
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.retireLocked()
	done := h.done
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

// running reports whether a watcher goroutine is active (for tests).
func (h *liveHub) running() bool {
	h.mu.Lock()
	done := h.done
	h.mu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
		return true
	}
}

func (h *liveHub) run(ctx context.Context, gen uint64, prev <-chan struct{}, done chan struct{}) {
	defer close(done)
	if prev != nil {
		select {
		case <-prev:
		case <-ctx.Done():
			return
		}
	}
	for {
		if up := h.followDaemon(ctx, gen); !up && ctx.Err() == nil {
			h.pollLocal(ctx, gen)
		}
		timer := time.NewTimer(h.cfg.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// followDaemon streams the daemon's alert events and samples until the
// stream ends. It reports whether the daemon is still reachable afterwards,
// so a stream that cannot be opened does not flap the daemon state.
func (h *liveHub) followDaemon(ctx context.Context, gen uint64) bool {
	client, ok := h.connect(ctx)
	if !ok {
		h.setDaemon(gen, false)
		return false
	}
	h.setDaemon(gen, true)
	var got atomic.Bool
	err := client.Subscribe(ctx, true,
		func(hostwatch.Event) { got.Store(true); h.setStreamErr(gen, ""); h.alertEvent(gen) },
		func(sample hostwatch.Sample) { got.Store(true); h.setStreamErr(gen, ""); h.observe(gen, sample, true) })
	_ = client.Close()
	if ctx.Err() != nil {
		return true
	}
	if err != nil {
		// The daemon accepted the connection but its stream failed (an older
		// or broken daemon, or a stream that broke mid-way): sample locally
		// and retry at the poll pace.
		h.setStreamErr(gen, err.Error())
		if !got.Load() {
			return false
		}
	}
	// Reachability is probed separately: a failed stream says nothing about
	// whether the daemon still answers requests.
	probe, ok := h.connect(ctx)
	if ok {
		_ = probe.Close()
		return err == nil
	}
	h.setDaemon(gen, false)
	return false
}

func (h *liveHub) pollLocal(ctx context.Context, gen uint64) {
	if !h.wants(gen, hostHealthURI) {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, localSampleTimeout)
	sample, err := h.cfg.sampleLocal(sctx)
	cancel()
	if err != nil {
		return
	}
	h.observe(gen, sample, false)
}

func (h *liveHub) wants(gen uint64, uri string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return gen == h.gen && h.counts[uri] > 0
}

// setDaemon records whether the daemon is reachable. A change after the
// first observation changes both resources' contents, so both are notified.
func (h *liveHub) setDaemon(gen uint64, up bool) {
	h.mu.Lock()
	if gen != h.gen || (h.daemonKnown && h.daemonUp == up) {
		h.mu.Unlock()
		return
	}
	changed := h.daemonKnown
	h.daemonKnown, h.daemonUp = true, up
	h.haveLevel = false
	var uris []string
	if changed {
		for _, uri := range []string{firingAlertsURI, hostHealthURI} {
			if h.counts[uri] > 0 {
				uris = append(uris, uri)
			}
		}
	}
	h.mu.Unlock()
	for _, uri := range uris {
		h.notify(uri)
	}
}

// setStreamErr records why the daemon's stream is unusable ("" once it
// delivers again). The reason changes the firing-alerts content, so a change
// notifies its subscribers.
func (h *liveHub) setStreamErr(gen uint64, reason string) {
	h.mu.Lock()
	if gen != h.gen || h.streamErr == reason {
		h.mu.Unlock()
		return
	}
	h.streamErr = reason
	send := h.counts[firingAlertsURI] > 0
	h.mu.Unlock()
	if send {
		h.notify(firingAlertsURI)
	}
}

// alertsUnavailable returns why the daemon's alert stream cannot be used.
func (h *liveHub) alertsUnavailable() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streamErr
}

// levelVerdict returns the load verdict the hub last notified for the given
// source, so resource content agrees with notifications.
func (h *liveHub) levelVerdict(daemon bool) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.haveLevel || h.counts[hostHealthURI] == 0 || h.level.daemon != daemon {
		return "", false
	}
	return h.level.loadVerdict, true
}

// alertEvent schedules one firing-alerts update per burst: the first event
// starts the debounce timer and later ones until it fires are absorbed.
func (h *liveHub) alertEvent(gen uint64) {
	h.mu.Lock()
	if gen != h.gen || h.counts[firingAlertsURI] == 0 || h.alertStop != nil {
		h.mu.Unlock()
		return
	}
	h.alertStop = h.cfg.afterFunc(h.cfg.alertDebounce, func() {
		h.mu.Lock()
		h.alertStop = nil
		h.mu.Unlock()
		if h.wants(gen, firingAlertsURI) {
			h.notify(firingAlertsURI)
		}
	})
	h.mu.Unlock()
}

// observe advances the health level with every sample and compares it with
// the last one notified, sending at most one host-health notification per
// healthMinInterval. A change held back by the interval is sent with the
// first later sample that still differs.
func (h *liveHub) observe(gen uint64, sample hostwatch.Sample, daemon bool) {
	now := h.now()
	h.mu.Lock()
	if gen != h.gen || h.counts[hostHealthURI] == 0 {
		h.mu.Unlock()
		return
	}
	if !daemon {
		h.localSample, h.localAt = sample, now
	}
	prev := ""
	if h.haveLevel {
		prev = h.level.loadVerdict
	}
	lvl := healthLevelOf(sample, prev, daemon)
	send := false
	switch {
	case !h.haveLevel:
		h.notified, h.haveLevel = lvl, true
	case lvl != h.notified && now.Sub(h.lastHealthNotify) >= h.cfg.healthMinInterval:
		h.notified, h.lastHealthNotify, send = lvl, now, true
	}
	h.level = lvl
	h.mu.Unlock()
	if send {
		h.notify(hostHealthURI)
	}
}

// cachedLocalSample returns the watcher's last local sample if it is younger
// than the poll interval, so reads between polls do not resample the host.
func (h *liveHub) cachedLocalSample() (hostwatch.Sample, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.localAt.IsZero() || h.now().Sub(h.localAt) >= h.cfg.pollInterval {
		return hostwatch.Sample{}, false
	}
	return h.localSample, true
}

func healthLevelOf(s hostwatch.Sample, prevVerdict string, daemon bool) healthLevel {
	return healthLevel{
		daemon:         daemon,
		loadVerdict:    loadVerdict(s.Load1, s.NCPU, prevVerdict),
		memoryPressure: s.MemoryPressure,
		thermal:        s.ThermalThrottled,
	}
}

// loadVerdict is "hold" when the 1-minute load per CPU reaches
// loadHoldPerCPU and "admit" otherwise. Once holding, it stays "hold" until
// load drops below loadReleasePerCPU.
func loadVerdict(load1 float64, ncpu int, prev string) string {
	per := loadPerCPU(load1, ncpu)
	if per >= loadHoldPerCPU || (prev == "hold" && per >= loadReleasePerCPU) {
		return "hold"
	}
	return "admit"
}

func loadPerCPU(load1 float64, ncpu int) float64 {
	if ncpu < 1 {
		ncpu = 1
	}
	return load1 / float64(ncpu)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
