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
	if c.Clock == nil {
		c.Clock = defaults.Clock
	}
	s.collect = c
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
	var result ToolResult
	switch params.Name {
	case "triage":
		result = s.toolTriage(params.Arguments)
	case "inspect_app":
		result = s.toolInspectApp(params.Arguments)
	case "snapshot":
		result = s.toolSnapshot(params.Arguments)
	case "diagnose":
		result = s.toolDiagnose(params.Arguments)
	case "process":
		result = s.toolProcess(params.Arguments)
	case "jvm":
		result = s.toolJVM(params.Arguments)
	case "network":
		result = s.toolNetwork(params.Arguments)
	case "toolchain":
		result = s.toolToolchain(params.Arguments)
	case "issues":
		result = s.toolIssues(params.Arguments)
	case "remote":
		result = s.toolRemote(params.Arguments)
	default:
		result = ToolResult{
			Content: []ContentBlock{{Type: "text", Text: "unknown tool: " + params.Name}},
			IsError: true,
		}
	}
	s.sendResponse(req.ID, nil, result)
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
