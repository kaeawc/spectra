package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestHandleInvalidParamsCode(t *testing.T) {
	d := NewDispatcher()
	d.Register("needs_params", func(_ json.RawMessage) (any, error) {
		return nil, InvalidParams("needs_params requires {\"id\":\"<id>\"}")
	})
	resp := d.handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"needs_params"}`))
	if resp.Error == nil {
		t.Fatal("expected an error response")
	}
	if resp.Error.Code != CodeInvalidParams {
		t.Fatalf("code = %d, want %d (CodeInvalidParams)", resp.Error.Code, CodeInvalidParams)
	}
	// The original message must be preserved verbatim.
	if resp.Error.Message != `needs_params requires {"id":"<id>"}` {
		t.Fatalf("message = %q, not preserved", resp.Error.Message)
	}
}

func TestHandleInternalErrorCodeUnchanged(t *testing.T) {
	d := NewDispatcher()
	d.Register("boom", func(_ json.RawMessage) (any, error) {
		return nil, fmt.Errorf("something broke internally")
	})
	resp := d.handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"boom"}`))
	if resp.Error == nil || resp.Error.Code != CodeInternalError {
		t.Fatalf("code = %v, want %d (CodeInternalError)", resp.Error, CodeInternalError)
	}
}

func TestInvalidParamsWrappingDetected(t *testing.T) {
	// A wrapped invalidParamsError must still be detected via errors.As.
	err := fmt.Errorf("context: %w", InvalidParams("bad %s", "id"))
	if !isInvalidParams(err) {
		t.Fatal("wrapped invalid-params error not detected")
	}
	if isInvalidParams(errors.New("plain")) {
		t.Fatal("plain error misdetected as invalid-params")
	}
}
