package netproto

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

const redactedHeaderValue = "[redacted]"

var sensitiveHTTPHeaders = map[string]bool{
	"authorization":       true,
	"cookie":              true,
	"proxy-authorization": true,
	"set-cookie":          true,
	"x-api-key":           true,
}

// HTTPMessage is a metadata-only summary of one HTTP/1.x request or response
// header block. Body bytes are intentionally ignored.
type HTTPMessage struct {
	IsRequest  bool         `json:"is_request"`
	Method     string       `json:"method,omitempty"`
	Target     string       `json:"target,omitempty"`
	StatusCode int          `json:"status_code,omitempty"`
	Reason     string       `json:"reason,omitempty"`
	Version    string       `json:"version,omitempty"`
	Headers    []HTTPHeader `json:"headers,omitempty"`
	Truncated  bool         `json:"truncated,omitempty"`
}

// HTTPHeader is one parsed HTTP header.
type HTTPHeader struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Redacted bool   `json:"redacted,omitempty"`
}

// ParseHTTP1Headers parses an HTTP/1.x header block. It stops at the first
// blank line and ignores any body bytes after the header terminator.
func ParseHTTP1Headers(data []byte) (HTTPMessage, error) {
	headerBlock, truncated := splitHTTPHeaderBlock(string(data))
	sc := bufio.NewScanner(strings.NewReader(headerBlock))
	sc.Buffer(make([]byte, 0, 4096), 256*1024)
	if !sc.Scan() {
		return HTTPMessage{}, fmt.Errorf("http header block is empty")
	}
	msg, err := parseHTTPStartLine(sc.Text())
	if err != nil {
		return HTTPMessage{}, err
	}
	msg.Truncated = truncated

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		header, ok := parseHTTPHeader(line)
		if ok {
			msg.Headers = append(msg.Headers, header)
		}
	}
	if err := sc.Err(); err != nil {
		return HTTPMessage{}, fmt.Errorf("scan http headers: %w", err)
	}
	return msg, nil
}

func splitHTTPHeaderBlock(data string) (string, bool) {
	if idx := strings.Index(data, "\r\n\r\n"); idx >= 0 {
		return data[:idx+2], false
	}
	if idx := strings.Index(data, "\n\n"); idx >= 0 {
		return data[:idx+1], false
	}
	return data, true
}

func parseHTTPStartLine(line string) (HTTPMessage, error) {
	if strings.HasPrefix(line, "HTTP/") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			return HTTPMessage{}, fmt.Errorf("malformed http response line")
		}
		code, err := strconv.Atoi(parts[1])
		if err != nil {
			return HTTPMessage{}, fmt.Errorf("malformed http status code: %w", err)
		}
		msg := HTTPMessage{Version: parts[0], StatusCode: code}
		if len(parts) == 3 {
			msg.Reason = parts[2]
		}
		return msg, nil
	}

	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "HTTP/") {
		return HTTPMessage{}, fmt.Errorf("malformed http request line")
	}
	return HTTPMessage{
		IsRequest: true,
		Method:    parts[0],
		Target:    parts[1],
		Version:   parts[2],
	}, nil
}

func parseHTTPHeader(line string) (HTTPHeader, bool) {
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return HTTPHeader{}, false
	}
	header := HTTPHeader{
		Name:  strings.TrimSpace(name),
		Value: strings.TrimSpace(value),
	}
	if sensitiveHTTPHeaders[strings.ToLower(header.Name)] {
		header.Value = redactedHeaderValue
		header.Redacted = true
	}
	return header, header.Name != ""
}
