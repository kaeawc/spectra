// Package electronfuse reads the "fuse" configuration Electron bakes into its
// framework binary. Fuses are build-time security toggles (RunAsNode, Node CLI
// inspect arguments, asar integrity validation, …). They are encoded as a
// sentinel string followed by a version byte, a fuse-count byte, and one ASCII
// status byte per fuse ('0' disabled, '1' enabled, else removed/inert). A
// dangerous fuse left enabled turns a signed app into a local code-injection
// surface. This package decodes the wire and assesses the security posture; it
// reads bytes only and never executes anything.
package electronfuse

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

// sentinel precedes the fuse wire in the Electron Framework binary.
var sentinel = []byte("dL7pKGdnNz796PbbjQWNKmHXBZaB9tsX")

// ErrNoSentinel means the fuse wire was not found (not an Electron binary, or a
// build without the fuse sentinel).
var ErrNoSentinel = errors.New("electronfuse: fuse sentinel not found")

// Status is one fuse's decoded state.
type Status string

const (
	StatusDisabled Status = "disabled"
	StatusEnabled  Status = "enabled"
	StatusRemoved  Status = "removed"
)

// Fuse is one decoded fuse.
type Fuse struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Status Status `json:"status"`
}

// Config is the decoded fuse wire.
type Config struct {
	Version int    `json:"version"`
	Fuses   []Fuse `json:"fuses"`
}

// v1Names are the schema-v1 fuse names in wire order (part of Electron's fuse
// ABI). Indices beyond this list are labeled generically.
var v1Names = []string{
	"RunAsNode",
	"EnableCookieEncryption",
	"EnableNodeOptionsEnvironmentVariable",
	"EnableNodeCliInspectArguments",
	"EnableEmbeddedAsarIntegrityValidation",
	"OnlyLoadAppFromAsar",
	"LoadBrowserProcessSpecificV8Snapshot",
	"GrantFileProtocolExtraPrivileges",
}

// ParseFile reads an Electron Framework binary and decodes its fuse wire.
func ParseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes the fuse wire from a binary's bytes.
func Parse(data []byte) (*Config, error) {
	i := bytes.Index(data, sentinel)
	if i < 0 {
		return nil, ErrNoSentinel
	}
	p := i + len(sentinel)
	if p+2 > len(data) {
		return nil, fmt.Errorf("electronfuse: truncated wire after sentinel")
	}
	version := int(data[p])
	count := int(data[p+1])
	start := p + 2
	if start+count > len(data) {
		return nil, fmt.Errorf("electronfuse: fuse count %d exceeds data", count)
	}
	cfg := &Config{Version: version}
	for idx := 0; idx < count; idx++ {
		cfg.Fuses = append(cfg.Fuses, Fuse{
			Index:  idx,
			Name:   fuseName(idx),
			Status: statusFromByte(data[start+idx]),
		})
	}
	return cfg, nil
}

func fuseName(idx int) string {
	if idx >= 0 && idx < len(v1Names) {
		return v1Names[idx]
	}
	return fmt.Sprintf("Fuse%d", idx)
}

func statusFromByte(b byte) Status {
	switch b {
	case '0':
		return StatusDisabled
	case '1':
		return StatusEnabled
	default:
		return StatusRemoved
	}
}

// Get returns the status of a named fuse.
func (c *Config) Get(name string) (Status, bool) {
	for _, f := range c.Fuses {
		if f.Name == name {
			return f.Status, true
		}
	}
	return "", false
}

// Finding is one security observation about a fuse configuration.
type Finding struct {
	Fuse     string `json:"fuse"`
	Severity string `json:"severity"` // critical, warn, info
	Message  string `json:"message"`
}

// Audit assesses the fuse configuration for local code-injection risk.
func (c *Config) Audit() []Finding {
	var out []Finding
	enabled := func(name string) bool {
		s, ok := c.Get(name)
		return ok && s == StatusEnabled
	}
	disabled := func(name string) bool {
		s, ok := c.Get(name)
		return ok && s == StatusDisabled
	}
	if enabled("RunAsNode") {
		out = append(out, Finding{"RunAsNode", "critical",
			"RunAsNode is enabled — any local process can set ELECTRON_RUN_AS_NODE and execute arbitrary JavaScript as this signed app."})
	}
	if enabled("EnableNodeCliInspectArguments") {
		out = append(out, Finding{"EnableNodeCliInspectArguments", "warn",
			"Node CLI --inspect arguments are honored — a local process can attach a debugger and run code in the app's context."})
	}
	if enabled("EnableNodeOptionsEnvironmentVariable") {
		out = append(out, Finding{"EnableNodeOptionsEnvironmentVariable", "warn",
			"NODE_OPTIONS is honored — a local process can inject Node flags or preload modules into the app."})
	}
	if disabled("EnableEmbeddedAsarIntegrityValidation") {
		out = append(out, Finding{"EnableEmbeddedAsarIntegrityValidation", "warn",
			"Embedded asar integrity validation is off — the app.asar payload can be swapped without detection."})
	}
	if disabled("OnlyLoadAppFromAsar") {
		out = append(out, Finding{"OnlyLoadAppFromAsar", "info",
			"OnlyLoadAppFromAsar is off — the app may load code from an unpacked directory, not just the signed asar."})
	}
	return out
}
