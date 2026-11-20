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
	// callMu serializes tool calls and resource reads. The SDK dispatches
	// requests concurrently; the handlers and the lazily connected daemon
	// client were written for one call at a time.
	callMu        sync.Mutex
	Version       string
	Verbose       bool
	log           logger.Logger
	collect       Collectors
	daemon        daemonWatchClient
	connectDaemon func(context.Context) (daemonWatchClient, bool)
	daemonTimeout time.Duration
}

// NewServer returns a configured MCP server from stdin/stdout handles.
func NewServer(reader io.Reader, writer io.Writer) *Server {
	return &Server{
		reader:  reader,
		writer:  writer,
		Version: "dev",
		log:     logger.New(logger.Config{Format: logger.FormatText, Level: slog.LevelInfo}),
		collect: defaultCollectors(),
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

// Run serves one MCP session over the reader and writer until the client
// closes its end or ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	err := s.sdkServer().Run(ctx, &sdk.IOTransport{
		Reader: io.NopCloser(s.reader),
		Writer: nopWriteCloser{s.writer},
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
	opts := &sdk.ServerOptions{
		// Advertise tools, resources and prompts without the SDK's default
		// logging capability; the surface never changes at runtime.
		Capabilities: &sdk.ServerCapabilities{
			Tools:     &sdk.ToolCapabilities{},
			Resources: &sdk.ResourceCapabilities{},
			Prompts:   &sdk.PromptCapabilities{},
		},
	}
	if s.Verbose {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	srv := sdk.NewServer(&sdk.Implementation{Name: "spectra-mcp", Version: s.Version}, opts)
	handlers := s.toolHandlers()
	for _, def := range toolDefinitions() {
		srv.AddTool(sdkTool(def), s.sdkToolHandler(handlers[def.Name]))
	}
	for _, r := range resourceDefinitions() {
		h := readStaticResource
		if r.URI == firingAlertsURI {
			h = s.readFiringAlerts
		}
		srv.AddResource(r, h)
	}
	for _, p := range promptDefinitions() {
		srv.AddPrompt(p, getPrompt)
	}
	return srv
}

// stateChangingTools may change host or Spectra state for some operation
// (snapshot store, issue status, alert ack, cache clear, JVM attach or MBean
// invoke, packet capture), so they carry no readOnlyHint. Every other tool
// only reads.
var stateChangingTools = map[string]bool{
	"snapshot": true,
	"diagnose": true,
	"jvm":      true,
	"network":  true,
	"issues":   true,
	"cache":    true,
	"alerts":   true,
}

func sdkTool(def ToolDefinition) *sdk.Tool {
	t := &sdk.Tool{Name: def.Name, Description: def.Description, InputSchema: def.InputSchema}
	if !stateChangingTools[def.Name] {
		t.Annotations = &sdk.ToolAnnotations{ReadOnlyHint: true}
	}
	return t
}

func (s *Server) sdkToolHandler(h func(json.RawMessage) ToolResult) sdk.ToolHandler {
	return func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		s.callMu.Lock()
		res := h(req.Params.Arguments)
		s.callMu.Unlock()
		out := &sdk.CallToolResult{IsError: res.IsError, Content: make([]sdk.Content, 0, len(res.Content))}
		for _, c := range res.Content {
			out.Content = append(out.Content, &sdk.TextContent{Text: c.Text})
		}
		return out, nil
	}
}

// toolHandlers maps each tool name to its handler. The table keeps
// handleToolsCall flat and makes the exposed surface easy to audit against
// toolDefinitions.
func (s *Server) toolHandlers() map[string]func(json.RawMessage) ToolResult {
	handlers := map[string]func(json.RawMessage) ToolResult{
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
