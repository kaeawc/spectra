package serve

import "testing"

func TestDaemonNetworkCaptureSummarizeRequiresPath(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 70, "network.capture.summarize", `{}`)
	if resp.Error == nil {
		t.Fatal("expected an error for missing path")
	}
	if resp.Error.Code != -32602 { // CodeInvalidParams
		t.Fatalf("code = %d, want -32602 (invalid params)", resp.Error.Code)
	}
}

func TestDaemonNetworkCaptureSummarizeOpenError(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 71, "network.capture.summarize", `{"path":"/nonexistent/path/to.pcap"}`)
	if resp.Error == nil {
		t.Fatal("expected an error opening a missing capture file")
	}
	// Missing file is a server-side failure, not an invalid-params error.
	if resp.Error.Code == -32602 {
		t.Fatalf("code = %d, want an internal error for a missing file", resp.Error.Code)
	}
}

// network.diagnose performs live network probing, so it is not exercised over the
// wire here (slow/flaky). Its param mapping is covered by the connect client test
// (TestParseConnectTypedCalls) and its logic by internal/netdiag's own tests.
