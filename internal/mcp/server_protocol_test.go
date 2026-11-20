package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/surface.golden.json from the running server")

// startSession runs a Server in-process over pipes, the same newline-framed
// stdio transport clients use, and connects an SDK client at version.
func startSession(t *testing.T, version string) *sdk.ClientSession {
	t.Helper()
	return startServerSession(t, NewServer(nil, nil), version, nil)
}

// startServerSession is startSession for a configured server; its reader and
// writer are replaced by the pipes.
func startServerSession(t *testing.T, srv *Server, version string, opts *sdk.ClientOptions) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	srv.reader, srv.writer = serverIn, serverOut
	srv.Version = "test"
	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
		_ = serverOut.Close()
	}()
	client := sdk.NewClient(&sdk.Implementation{Name: "spectra-test", Version: "0"}, opts)
	cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientIn, Writer: clientOut}, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect at %s: %v", version, err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = clientOut.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after the client closed")
		}
	})
	return cs
}

// surfaceTool is the part of a listed tool the golden pins.
type surfaceTool struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	InputSchema  any    `json:"inputSchema"`
	OutputSchema any    `json:"outputSchema,omitempty"`
}

type surface struct {
	Tools     []any `json:"tools"`
	Resources []any `json:"resources"`
	Prompts   []any `json:"prompts"`
}

// listSurface returns the tools (name, description, inputSchema and
// outputSchema only), resources and prompts the server lists, sorted, as generic JSON values.
func listSurface(t *testing.T, cs *sdk.ClientSession) surface {
	t.Helper()
	ctx := context.Background()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	resources, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	prompts, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("prompts/list: %v", err)
	}
	sort.Slice(tools.Tools, func(i, j int) bool { return tools.Tools[i].Name < tools.Tools[j].Name })
	sort.Slice(resources.Resources, func(i, j int) bool { return resources.Resources[i].URI < resources.Resources[j].URI })
	sort.Slice(prompts.Prompts, func(i, j int) bool { return prompts.Prompts[i].Name < prompts.Prompts[j].Name })
	var out surface
	for _, tool := range tools.Tools {
		out.Tools = append(out.Tools, toGeneric(t, surfaceTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, OutputSchema: tool.OutputSchema}))
	}
	for _, r := range resources.Resources {
		out.Resources = append(out.Resources, toGeneric(t, r))
	}
	for _, p := range prompts.Prompts {
		out.Prompts = append(out.Prompts, toGeneric(t, p))
	}
	return out
}

func toGeneric(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSurfaceMatchesGolden pins every tool name, description, input schema
// (unchanged from the pre-SDK server) and output schema, and every resource
// and prompt, at each protocol revision a client can negotiate.
func TestSurfaceMatchesGolden(t *testing.T) {
	path := filepath.Join("testdata", "surface.golden.json")
	for _, version := range sdk.SupportedProtocolVersions() {
		t.Run(version, func(t *testing.T) {
			got := listSurface(t, startSession(t, version))
			if *updateGolden && version == sdk.SupportedProtocolVersions()[0] {
				data, err := json.MarshalIndent(got, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want surface
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				g, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("surface differs from %s (run with -update to accept):\n%s", path, g)
			}
		})
	}
}

func TestToolAnnotationsMarkReadOnlyTools(t *testing.T) {
	tools, err := startSession(t, "2025-11-25").ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		if readOnly == stateChangingTools[tool.Name] {
			t.Errorf("%s: readOnlyHint = %v, state-changing = %v", tool.Name, readOnly, stateChangingTools[tool.Name])
		}
	}
	for name := range stateChangingTools {
		if _, ok := (&Server{}).toolHandlers()[name]; !ok {
			t.Errorf("stateChangingTools names unknown tool %q", name)
		}
	}
}

// TestStdioEndToEnd negotiates the handshake revision and the stateless
// revision, then lists tools and calls lineage on this test process.
func TestStdioEndToEnd(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			cs := startSession(t, version)
			init := cs.InitializeResult()
			if init.ProtocolVersion != version {
				t.Fatalf("negotiated %s, want %s", init.ProtocolVersion, version)
			}
			if init.ServerInfo == nil || init.ServerInfo.Name != "spectra-mcp" || init.ServerInfo.Version != "test" {
				t.Fatalf("server info = %+v", init.ServerInfo)
			}
			tools, err := cs.ListTools(context.Background(), nil)
			if err != nil || len(tools.Tools) != len(toolDefinitions()) {
				t.Fatalf("tools/list: %d tools, err %v", len(tools.Tools), err)
			}
			pid := os.Getpid()
			res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "lineage", Arguments: map[string]any{"pid": pid}})
			if err != nil {
				t.Fatalf("tools/call lineage: %v", err)
			}
			if res.IsError || len(res.Content) != 1 {
				t.Fatalf("lineage result: %+v", res)
			}
			text, ok := res.Content[0].(*sdk.TextContent)
			if !ok || !strings.Contains(text.Text, fmt.Sprintf(`"pid": %d`, pid)) {
				t.Fatalf("lineage content does not name pid %d: %+v", pid, res.Content[0])
			}
			bad, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "lineage", Arguments: map[string]any{}})
			if err != nil || !bad.IsError {
				t.Fatalf("lineage without pid: result %+v, err %v; want an isError result", bad, err)
			}
		})
	}
}

// rawSession drives the server with hand-written JSON lines, as an older
// client that is not built on the SDK would.
func rawSession(t *testing.T) (send func(string), recv func() map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	go func() {
		_ = NewServer(serverIn, serverOut).Run(ctx)
		_ = serverOut.Close()
	}()
	t.Cleanup(func() { _ = clientOut.Close(); cancel() })
	lines := bufio.NewScanner(clientIn)
	lines.Buffer(make([]byte, 0, 1<<20), 16<<20)
	send = func(line string) {
		if _, err := io.WriteString(clientOut, line+"\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	recv = func() map[string]any {
		if !lines.Scan() {
			t.Fatalf("no response: %v", lines.Err())
		}
		var m map[string]any
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			t.Fatalf("response %q is not JSON: %v", lines.Text(), err)
		}
		return m
	}
	return send, recv
}

func TestLegacyInitializeAndPing(t *testing.T) {
	send, recv := rawSession(t)
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`)
	init := recv()
	result, _ := init["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize = %v", init)
	}
	caps, _ := result["capabilities"].(map[string]any)
	for _, c := range []string{"tools", "resources", "prompts"} {
		if _, ok := caps[c]; !ok {
			t.Errorf("capability %s missing: %v", c, caps)
		}
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","method":"no/such/notification"}`)
	send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if got := recv(); got["id"] != float64(3) || got["error"] != nil {
		t.Fatalf("ping response = %v, want only the ping reply", got)
	}
	send(`{"jsonrpc":"2.0","id":4,"method":"no/such/method"}`)
	if got := recv(); got["id"] != float64(4) || got["error"] == nil {
		t.Fatalf("unknown method response = %v, want an error", got)
	}
}
