package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/logger"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server serves Spectra's tools, resources and prompts over stdio through
// the MCP Go SDK, which negotiates every protocol revision it supports
// (2024-11-05 through 2026-07-28).
type Server struct {
	reader io.Reader
	writer io.Writer
	// callSlot serializes tool calls and resource reads. The SDK dispatches
	// requests concurrently; the handlers and the lazily connected daemon
	// client were written for one call at a time. It is a 1-slot channel
	// rather than a mutex so a call cancelled while queued gives up its turn.
	callSlot chan struct{}
	slotOnce sync.Once
	// onQueued, when set, runs as a call starts waiting for a busy slot
	// (tests use it to cancel a queued call deterministically).
	onQueued func()
	// onQueuedCancelled, when set, runs when a queued call gives up because
	// its context ended. Tests use it to order a cancel before the slot frees.
	onQueuedCancelled func()
	// eofWait bounds how long Run keeps the session open after its input
	// closes so in-flight calls can still reply.
	eofWait time.Duration
	// progressEvery is the period of time-based progress notifications.
	progressEvery time.Duration
	Version       string
	Verbose       bool
	log           logger.Logger
	collect       Collectors
	daemon        daemonWatchClient
	connectDaemon func(context.Context) (daemonWatchClient, bool)
	daemonTimeout time.Duration
	// live serves resource subscriptions for the current Run; liveCfg
	// overrides its intervals and local sampler (mainly for tests).
	live    *liveHub
	liveCfg liveConfig
	// artifactDir overrides where tool artifacts are stored and served from;
	// homeDir overrides how the default ~/.spectra/artifacts is found.
	artifactDir string
	homeDir     func() (string, error)
	// artifactKindBytes overrides the per-kind retention size (tests).
	artifactKindBytes int64
	// wallClock times progress, job progress and completion caches. It is
	// wall time even when collect.Clock is fixed; nil is the system clock.
	wallClock Clock
	// skipWarm leaves the completion caches cold at start (tests that count
	// collector calls).
	skipWarm    bool
	completions completionCache
	// jobs runs job_start's background tool calls for the current Run;
	// jobsCfg overrides its limits and clock (mainly for tests).
	jobs    *jobStore
	jobsCfg jobConfig
}

// NewServer returns a configured MCP server from stdin/stdout handles.
func NewServer(reader io.Reader, writer io.Writer) *Server {
	return &Server{
		reader:  reader,
		writer:  writer,
		Version: "dev",
		log:     logger.New(logger.Config{Format: logger.FormatText, Level: slog.LevelInfo}),
		collect: defaultCollectors(),
		eofWait: defaultEOFWait,
	}
}

// SetLogger overrides log output (mainly for tests).
func (s *Server) SetLogger(l logger.Logger) { s.log = l }

// SetCollectors overrides host-inspection dependencies (mainly for tests).
// Zero fields keep their default implementation.
func (s *Server) SetCollectors(c Collectors) {
	defaults := defaultCollectors()
	c = mergeInspectionCollectors(c, defaults)
	c = mergeHostStateCollectors(c, defaults)
	c.Reports = mergeReportDeps(c.Reports, defaults.Reports)
	if c.Clock == nil {
		c.Clock = defaults.Clock
	}
	s.collect = c
}

// mergeInspectionCollectors fills the app/process/JVM inspection collectors
// from defaults where the caller left them nil.
func mergeInspectionCollectors(c, defaults Collectors) Collectors {
	if c.Apps == nil {
		c.Apps = defaults.Apps
	}
	if c.Processes == nil {
		c.Processes = defaults.Processes
	}
	if c.Network == nil {
		c.Network = defaults.Network
	}
	if c.Snapshots == nil {
		c.Snapshots = defaults.Snapshots
	}
	if c.JVMs == nil {
		c.JVMs = defaults.JVMs
	}
	if c.Toolchain == nil {
		c.Toolchain = defaults.Toolchain
	}
	if c.DB == nil {
		c.DB = defaults.DB
	}
	return c
}

// mergeHostStateCollectors fills the host-state collectors from defaults where
// the caller left them nil.
func mergeHostStateCollectors(c, defaults Collectors) Collectors {
	if c.Power == nil {
		c.Power = defaults.Power
	}
	if c.Memory == nil {
		c.Memory = defaults.Memory
	}
	if c.Storage == nil {
		c.Storage = defaults.Storage
	}
	if c.System == nil {
		c.System = defaults.System
	}
	if c.Services == nil {
		c.Services = defaults.Services
	}
	if c.Logs == nil {
		c.Logs = defaults.Logs
	}
	if c.Updates == nil {
		c.Updates = defaults.Updates
	}
	if c.TimeMachine == nil {
		c.TimeMachine = defaults.TimeMachine
	}
	if c.CoreFiles == nil {
		c.CoreFiles = defaults.CoreFiles
	}
	if c.Playbooks == nil {
		c.Playbooks = defaults.Playbooks
	}
	return c
}

// defaultEOFWait bounds how long a session whose input has closed waits for
// in-flight calls to reply.
const defaultEOFWait = 30 * time.Second

// Run serves one MCP session over the reader and writer until the client
// closes its end or ctx is cancelled. When the input closes with calls still
// running, Run waits (up to eofWait) for their replies before returning, so
// piped sessions get every answer. Replies to cancelled requests are never
// written.
func (s *Server) Run(ctx context.Context) error {
	srv := s.sdkServer()
	defer s.live.close()
	defer s.jobs.close()
	s.warmCompletions(ctx)
	tracker := newReplyTracker()
	err := srv.Run(ctx, &sdk.IOTransport{
		Reader: io.NopCloser(newTrackingReader(ctx, s.reader, tracker, s.eofWait)),
		Writer: nopWriteCloser{&replyWriter{w: s.writer, t: tracker}},
	})
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("mcp session: %w", err)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// sdkServer builds the SDK server with every Spectra tool, resource and prompt.
func (s *Server) sdkServer() *sdk.Server {
	hub := newLiveHub(s.liveCfg, streamConnector(s.connectDaemon), s.now)
	opts := &sdk.ServerOptions{
		// Advertise tools, resources and prompts without the SDK's default
		// logging capability; the surface never changes at runtime.
		// Resource subscriptions make alerts and host health live.
		Capabilities: &sdk.ServerCapabilities{
			Tools:     &sdk.ToolCapabilities{},
			Resources: &sdk.ResourceCapabilities{Subscribe: true},
			Prompts:   &sdk.PromptCapabilities{},
		},
		SubscribeHandler:   hub.subscribe,
		UnsubscribeHandler: hub.unsubscribe,
		CompletionHandler:  s.complete,
	}
	if s.Verbose {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "spectra-mcp", Version: s.Version}, opts)
	// ResourceUpdated reaches legacy sessions on the session channel and
	// 2026-07-28 sessions only on their subscriptions/listen stream.
	hub.notify = func(uri string) {
		_ = srv.ResourceUpdated(context.Background(), &sdk.ResourceUpdatedNotificationParams{URI: uri})
	}
	s.live = hub
	handlers := s.toolHandlers()
	for _, def := range toolDefinitions() {
		srv.AddTool(sdkTool(def), s.sdkToolHandler(handlers[def.Name]))
	}
	jobsCfg := s.jobsCfg
	if jobsCfg.ProgressClock == nil {
		jobsCfg.ProgressClock = s.wall()
	}
	s.jobs = newJobStore(jobsCfg, s.collect.Clock)
	for _, def := range jobToolDefinitions() {
		srv.AddTool(jobSDKTool(def), s.jobToolHandler(def.Name))
	}
	for _, r := range resourceDefinitions() {
		srv.AddResource(r, s.resourceHandler(r.URI))
	}
	for _, t := range artifactTemplates() {
		srv.AddResourceTemplate(t, s.readArtifact)
	}
	for _, p := range promptDefinitions() {
		srv.AddPrompt(p, getPrompt)
	}
	return srv
}

// stateChangingTools may change host or Spectra state for some operation
// (snapshot store, issue status, alert ack, cache clear, JVM attach or MBean
// invoke, packet capture, and triage's JVM and file-descriptor samples saved
// to the local store), so they carry no readOnlyHint. Every other tool only
// reads.
var stateChangingTools = map[string]bool{
	"snapshot": true,
	"diagnose": true,
	"jvm":      true,
	"network":  true,
	"issues":   true,
	"cache":    true,
	"alerts":   true,
	"triage":   true,
}

func sdkTool(def ToolDefinition) *sdk.Tool {
	t := &sdk.Tool{Name: def.Name, Description: def.Description, InputSchema: def.InputSchema, OutputSchema: outputSchema(def.Name)}
	if !stateChangingTools[def.Name] {
		t.Annotations = &sdk.ToolAnnotations{ReadOnlyHint: true}
	}
	return t
}

// toolHandler runs one tool. ctx is the request's context: it is cancelled
// when the client cancels the call, and carries the call's progress reporter.
type toolHandler func(ctx context.Context, raw json.RawMessage) ToolResult

func (s *Server) sdkToolHandler(h toolHandler) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		release, err := s.acquire(ctx)
		if err != nil {
			return nil, err
		}
		res := h(withProgress(ctx, s.newProgress(req)), req.Params.Arguments)
		release()
		if err := ctx.Err(); err != nil {
			// The client cancelled; the transport drops this reply.
			return nil, err
		}
		return sdkResult(res), nil
	}
}

// sdkResult converts a tool's result to the wire form: text content, then
// resource links, then structuredContent on success. Direct calls and
// job_result share it so a job's result matches a direct call's.
func sdkResult(res ToolResult) *sdk.CallToolResult {
	out := &sdk.CallToolResult{IsError: res.IsError, Content: make([]sdk.Content, 0, len(res.Content)+len(res.Links))}
	for _, c := range res.Content {
		out.Content = append(out.Content, &sdk.TextContent{Text: c.Text})
	}
	for _, l := range res.Links {
		out.Content = append(out.Content, l.content())
	}
	if !res.IsError && res.Structured != nil {
		out.StructuredContent = res.Structured
	}
	return out
}

func (s *Server) wall() Clock {
	if s.wallClock != nil {
		return s.wallClock
	}
	return systemClock{}
}

func (s *Server) slot() chan struct{} {
	s.slotOnce.Do(func() { s.callSlot = make(chan struct{}, 1) })
	return s.callSlot
}

// acquire waits for the call slot or for ctx to end, and returns the release
// function. A call cancelled while queued never runs.
func (s *Server) acquire(ctx context.Context) (func(), error) {
	slot := s.slot()
	select {
	case slot <- struct{}{}:
	default:
		if s.onQueued != nil {
			s.onQueued()
		}
		select {
		case slot <- struct{}{}:
		case <-ctx.Done():
			if s.onQueuedCancelled != nil {
				s.onQueuedCancelled()
			}
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		<-slot
		return nil, err
	}
	return func() { <-slot }, nil
}

// toolHandlers maps each tool name to its handler. The table keeps
// handleToolsCall flat and makes the exposed surface easy to audit against
// toolDefinitions.
func (s *Server) toolHandlers() map[string]toolHandler {
	handlers := map[string]toolHandler{
		"triage":      s.toolTriage,
		"inspect_app": s.toolInspectApp,
		"snapshot":    s.toolSnapshot,
		"diagnose":    s.toolDiagnose,
		"process":     s.toolProcess,
		"jvm":         s.toolJVM,
		"network":     s.toolNetwork,
		"db":          s.toolDB,
		"toolchain":   s.toolToolchain,
		"issues":      s.toolIssues,
		"power":       s.toolPower,
		"memory":      s.toolMemory,
		"storage":     s.toolStorage,
		"system":      s.toolSystem,
		"services":    s.toolServices,
		"logs":        s.toolLogs,
		"updates":     s.toolUpdates,
		"timemachine": s.toolTimeMachine,
		"playbook":    s.toolPlaybook,
		"cache":       s.toolCache,
		"core":        s.toolCore,
		"metrics":     s.toolMetrics,
		"host_health": s.toolHostHealth,
		"alerts":      s.toolAlerts,
	}
	for name, h := range s.reportToolHandlers() {
		handlers[name] = h
	}
	return handlers
}
