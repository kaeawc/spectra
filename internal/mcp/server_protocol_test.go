package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// runLines feeds newline-delimited messages through Server.Run and returns
// each response line decoded.
func runLines(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	s := NewServer(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out)
	s.Run()
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("response %q is not JSON: %v", l, err)
		}
		got = append(got, m)
	}
	return got
}

func errCode(m map[string]any) float64 {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return c
}

func TestRunAnswersMalformedMessagesWithNullID(t *testing.T) {
	cases := []struct {
		in     string
		code   float64
		wantID any
	}{
		{`{bad json`, -32700, nil},
		{`[]`, -32600, nil},
		{`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, -32600, nil},
		{`{"jsonrpc":"1.0","id":7,"method":"ping"}`, -32600, float64(7)},
		{`{"jsonrpc":"2.0","id":null,"method":"nope"}`, -32601, nil},
	}
	for _, tc := range cases {
		got := runLines(t, tc.in)
		if len(got) != 1 {
			t.Fatalf("%s: got %d responses, want 1", tc.in, len(got))
		}
		if c := errCode(got[0]); c != tc.code {
			t.Errorf("%s: error code = %v, want %v", tc.in, c, tc.code)
		}
		if id, present := got[0]["id"]; !present || id != tc.wantID {
			t.Errorf("%s: id = %v (present %v), want %v", tc.in, id, present, tc.wantID)
		}
	}
}

func TestRunPing(t *testing.T) {
	got := runLines(t, `{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if len(got) != 1 || got[0]["id"] != float64(3) || got[0]["error"] != nil {
		t.Fatalf("ping response = %v", got)
	}
	if r, ok := got[0]["result"].(map[string]any); !ok || len(r) != 0 {
		t.Fatalf("ping result = %v, want {}", got[0]["result"])
	}
}

func TestRunNotificationsGetNoResponse(t *testing.T) {
	got := runLines(t,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"no/such/method"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
	)
	if len(got) != 1 || got[0]["id"] != float64(1) {
		t.Fatalf("responses = %v, want only the ping reply", got)
	}
}
