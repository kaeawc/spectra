package netproto

import "testing"

func TestParseHTTP1RequestHeadersRedactsSensitiveValues(t *testing.T) {
	raw := []byte("GET /v1/search?q=spectra HTTP/1.1\r\nHost: api.example.com\r\nAuthorization: Bearer secret\r\nCookie: sid=abc\r\nAccept: application/json\r\n\r\nbody=ignored")

	msg, err := ParseHTTP1Headers(raw)
	if err != nil {
		t.Fatalf("ParseHTTP1Headers: %v", err)
	}
	if !msg.IsRequest || msg.Method != "GET" || msg.Target != "/v1/search?q=spectra" || msg.Version != "HTTP/1.1" {
		t.Fatalf("request line = %+v", msg)
	}
	if msg.Truncated {
		t.Fatal("Truncated = true, want false")
	}
	headers := headersByName(msg.Headers)
	if headers["Host"].Value != "api.example.com" || headers["Host"].Redacted {
		t.Fatalf("Host header = %+v", headers["Host"])
	}
	for _, name := range []string{"Authorization", "Cookie"} {
		h := headers[name]
		if h.Value != redactedHeaderValue || !h.Redacted {
			t.Fatalf("%s header = %+v, want redacted", name, h)
		}
	}
}

func TestParseHTTP1ResponseHeaders(t *testing.T) {
	raw := []byte("HTTP/1.1 101 Switching Protocols\nUpgrade: websocket\nSet-Cookie: sid=secret\n\nwebsocket bytes")

	msg, err := ParseHTTP1Headers(raw)
	if err != nil {
		t.Fatalf("ParseHTTP1Headers: %v", err)
	}
	if msg.IsRequest || msg.Version != "HTTP/1.1" || msg.StatusCode != 101 || msg.Reason != "Switching Protocols" {
		t.Fatalf("response line = %+v", msg)
	}
	headers := headersByName(msg.Headers)
	if headers["Upgrade"].Value != "websocket" {
		t.Fatalf("Upgrade header = %+v", headers["Upgrade"])
	}
	if headers["Set-Cookie"].Value != redactedHeaderValue || !headers["Set-Cookie"].Redacted {
		t.Fatalf("Set-Cookie header = %+v, want redacted", headers["Set-Cookie"])
	}
}

func TestParseHTTP1HeadersTruncated(t *testing.T) {
	msg, err := ParseHTTP1Headers([]byte("POST /upload HTTP/1.1\r\nHost: example.com\r\nContent-Length: 100"))
	if err != nil {
		t.Fatalf("ParseHTTP1Headers: %v", err)
	}
	if !msg.Truncated {
		t.Fatal("Truncated = false, want true")
	}
}

func TestParseHTTP1HeadersRejectsMalformedStartLine(t *testing.T) {
	if _, err := ParseHTTP1Headers([]byte("not http\r\nHost: example.com\r\n\r\n")); err == nil {
		t.Fatal("expected malformed request line error")
	}
	if _, err := ParseHTTP1Headers([]byte("HTTP/1.1 nope\r\n\r\n")); err == nil {
		t.Fatal("expected malformed status code error")
	}
}

func headersByName(headers []HTTPHeader) map[string]HTTPHeader {
	out := make(map[string]HTTPHeader, len(headers))
	for _, h := range headers {
		out[h.Name] = h
	}
	return out
}
