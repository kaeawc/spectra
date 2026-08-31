// Package symbolicate resolves raw stack addresses to symbol + file:line using
// the macOS `atos` tool. It is the enabling primitive for the native-capture
// features (spindump, flamegraph, hang-capture), which produce raw addresses
// that are meaningless until symbolicated. It reads only.
package symbolicate

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Request describes what to symbolicate.
type Request struct {
	// Binary is the path to the Mach-O image or its .dSYM.
	Binary string
	// LoadAddress is the address at which Binary was loaded (hex, e.g. 0x1049f4000).
	LoadAddress string
	// Arch optionally selects a slice of a universal binary (e.g. "arm64").
	Arch string
	// Addresses are the raw addresses to resolve.
	Addresses []string
}

// Runner executes a command and returns its combined stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Frame is one resolved (or unresolved) address.
type Frame struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol,omitempty"`
	Location string `json:"location,omitempty"` // file:line
	Raw      string `json:"raw"`
	Resolved bool   `json:"resolved"`
}

// Result is the symbolication of a whole request.
type Result struct {
	Binary      string  `json:"binary"`
	LoadAddress string  `json:"load_address"`
	Arch        string  `json:"arch,omitempty"`
	Frames      []Frame `json:"frames"`
}

// locationPattern captures the trailing "(File.ext:123)" atos appends.
var locationPattern = regexp.MustCompile(`\(([^()]+:\d+)\)\s*$`)

// inImagePattern matches the " (in Image)" qualifier atos inserts after the
// symbol, which must be removed without dropping a trailing "+ offset".
var inImagePattern = regexp.MustCompile(`\s*\(in [^)]*\)`)

// Symbolicate resolves req.Addresses against req.Binary via atos (run through
// runner) and returns a frame per address, in order.
func Symbolicate(ctx context.Context, req Request, runner Runner) (Result, error) {
	if req.Binary == "" {
		return Result{}, fmt.Errorf("symbolicate: a binary or dSYM path is required")
	}
	if req.LoadAddress == "" {
		return Result{}, fmt.Errorf("symbolicate: a load address is required")
	}
	if !isHexAddress(req.LoadAddress) {
		return Result{}, fmt.Errorf("symbolicate: invalid load address %q (want a hex value like 0x10a400000)", req.LoadAddress)
	}
	if len(req.Addresses) == 0 {
		return Result{}, fmt.Errorf("symbolicate: at least one address is required")
	}

	args := []string{"-o", req.Binary, "-l", req.LoadAddress}
	if req.Arch != "" {
		args = append(args, "-arch", req.Arch)
	}
	args = append(args, req.Addresses...)

	out, err := runner(ctx, "atos", args...)
	if err != nil {
		return Result{}, fmt.Errorf("symbolicate: atos failed: %w", err)
	}

	res := Result{Binary: req.Binary, LoadAddress: req.LoadAddress, Arch: req.Arch}
	res.Frames = parseFrames(req.Addresses, string(out))
	return res, nil
}

// parseFrames pairs each input address with atos's corresponding output line.
// atos emits exactly one line per input address, in order, so blank lines must
// be preserved as unresolved placeholders to keep the pairing aligned.
func parseFrames(addresses []string, out string) []Frame {
	lines := atosLines(out)
	frames := make([]Frame, len(addresses))
	for i, addr := range addresses {
		f := Frame{Address: addr}
		if i < len(lines) {
			f.Raw = lines[i]
			f.Symbol, f.Location, f.Resolved = parseLine(addr, lines[i])
		}
		frames[i] = f
	}
	return frames
}

// parseLine splits one atos output line into a symbol and a file:line location.
// atos echoes the raw address back when it cannot resolve it.
func parseLine(addr, line string) (symbol, location string, resolved bool) {
	line = strings.TrimSpace(line)
	if line == "" || line == addr {
		return "", "", false
	}
	if m := locationPattern.FindStringSubmatch(line); m != nil {
		location = m[1]
		line = strings.TrimSpace(locationPattern.ReplaceAllString(line, ""))
	}
	// Drop the "(in Image)" qualifier atos inserts between the symbol and any
	// trailing "+ offset", keeping the offset.
	line = strings.TrimSpace(inImagePattern.ReplaceAllString(line, ""))
	symbol = line
	resolved = symbol != "" || location != ""
	return symbol, location, resolved
}

// atosLines splits atos output into per-address lines, dropping only the
// trailing newline. Internal blank lines are kept so a blank line for one
// address does not shift every later address onto the wrong symbol.
func atosLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// isHexAddress reports whether s is a hexadecimal address, with an optional
// 0x prefix.
func isHexAddress(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return false
	}
	_, err := strconv.ParseUint(s, 16, 64)
	return err == nil
}
