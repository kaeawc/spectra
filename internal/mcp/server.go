// Package mcp implements a Spectra MCP server over stdio.
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/kaeawc/spectra/internal/jsonrpc"
	"github.com/kaeawc/spectra/internal/logger"
)

const protocolVersion = "2024-11-05"

// Server routes MCP JSON-RPC calls.
type Server struct {
	reader  *bufio.Reader
	writer  io.Writer
	mu      sync.Mutex
	Version string
	Verbose bool
	log     logger.Logger
	collect Collectors
}

// NewServer returns a configured MCP server from stdin/stdout handles.
func NewServer(reader io.Reader, writer io.Writer) *Server {
	return &Server{
		reader:  bufio.NewReader(reader),
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

// Run reads framed messages and processes them until EOF.
func (s *Server) Run() {
	for {
		msg, err := jsonrpc.ReadMessage(s.reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			s.log.Error("jsonrpc read failed", "err", err)
			return
		}
		var req Request
		if err := json.Unmarshal(msg, &req); err != nil {
			s.sendResponse(nil, &RPCError{Code: -32700, Message: "parse error: " + err.Error()})
			continue
		}
		if req.JSONRPC != "2.0" {
			s.sendResponse(req.ID, &RPCError{Code: -32600, Message: "invalid request"})
			continue
		}
		s.route(req)
	}
}

func (s *Server) sendResponse(id interface{}, rpcErr *RPCError, result ...any) {
	var payload any
	if len(result) == 1 {
		payload = result[0]
	}
	if id == nil {
		return
	}
	jsonrpc.SendResponse(s.writer, &s.mu, id, payload, rpcErr)
}

func (s *Server) route(req Request) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolsCall(req)
	case "resources/list":
		s.handleResourcesList(req)
	case "resources/read":
		s.handleResourcesRead(req)
	case "prompts/list":
		s.handlePromptsList(req)
	case "prompts/get":
		s.handlePromptsGet(req)
	case "shutdown":
		s.sendResponse(req.ID, nil, map[string]bool{"ok": true})
	case "exit":
		s.sendResponse(req.ID, nil, map[string]bool{"ok": true})
	default:
		s.sendResponse(req.ID, &RPCError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

func (s *Server) handleInitialize(req Request) {
	var params InitializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.sendResponse(req.ID, &RPCError{Code: -32602, Message: "invalid params: " + err.Error()})
			return
		}
		if params.ProtocolVersion != "" && params.ProtocolVersion != protocolVersion {
			s.log.Info("initialize with unsupported protocol", "protocol", params.ProtocolVersion)
		}
		_ = params
	}
	_ = req
	s.sendResponse(req.ID, nil, InitializeResult{
		ProtocolVersion: protocolVersion,
		ServerInfo: ServerInfo{
			Name:    "spectra-mcp",
			Version: s.Version,
		},
		Capabilities: ServerCaps{
			Tools:     &ToolsCap{},
			Resources: &ResourcesCap{},
			Prompts:   &PromptsCap{},
		},
	})
}

func (s *Server) handleToolsList(req Request) {
	s.sendResponse(req.ID, nil, ToolsListResult{
		Tools: toolDefinitions(),
	})
}

func (s *Server) handleToolsCall(req Request) {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendResponse(req.ID, &RPCError{Code: -32602, Message: "invalid params: " + err.Error()})
		return
	}
	handler, ok := s.toolHandlers()[params.Name]
	if !ok {
		s.sendResponse(req.ID, nil, ToolResult{
			Content: []ContentBlock{{Type: "text", Text: "unknown tool: " + params.Name}},
			IsError: true,
		})
		return
	}
	s.sendResponse(req.ID, nil, handler(params.Arguments))
}

// toolHandlers maps each tool name to its handler. The table keeps
// handleToolsCall flat and makes the exposed surface easy to audit against
// toolDefinitions.
func (s *Server) toolHandlers() map[string]func(json.RawMessage) ToolResult {
	return map[string]func(json.RawMessage) ToolResult{
		"triage":      s.toolTriage,
		"inspect_app": s.toolInspectApp,
		"snapshot":    s.toolSnapshot,
		"diagnose":    s.toolDiagnose,
		"process":     s.toolProcess,
		"jvm":         s.toolJVM,
		"network":     s.toolNetwork,
		"toolchain":   s.toolToolchain,
		"issues":      s.toolIssues,
		"remote":      s.toolRemote,
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
	}
}

func (s *Server) handleResourcesList(req Request) {
	s.sendResponse(req.ID, nil, ResourcesListResult{
		Resources: resourceDefinitions(),
	})
}

func (s *Server) handleResourcesRead(req Request) {
	var params ResourceReadParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendResponse(req.ID, &RPCError{Code: -32602, Message: "invalid params: " + err.Error()})
		return
	}
	content, mimeType, err := readResource(params.URI)
	if err != nil {
		s.sendResponse(req.ID, &RPCError{Code: -32602, Message: err.Error()})
		return
	}
	s.sendResponse(req.ID, nil, ResourceReadResult{
		Contents: []ResourceContent{{
			URI:      params.URI,
			MimeType: mimeType,
			Text:     content,
		}},
	})
}

func (s *Server) handlePromptsList(req Request) {
	s.sendResponse(req.ID, nil, PromptsListResult{
		Prompts: promptDefinitions(),
	})
}

func (s *Server) handlePromptsGet(req Request) {
	var params PromptGetParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendResponse(req.ID, &RPCError{Code: -32602, Message: "invalid params: " + err.Error()})
		return
	}
	result, err := s.getPrompt(params.Name, params.Arguments)
	if err != nil {
		s.sendResponse(req.ID, err)
		return
	}
	s.sendResponse(req.ID, nil, result)
}
