package jvm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultNMTSampleTimeout bounds the jcmd VM.native_memory summary call made
// while sampling. A hung attach must not stall the whole collection.
const DefaultNMTSampleTimeout = 5 * time.Second

// ErrNMTSampleTimeout means the bounded NMT summary call did not return in time.
var ErrNMTSampleTimeout = errors.New("native memory summary timed out")

// NMTEnabledInArgs reports whether the JVM was started with
// -XX:NativeMemoryTracking=summary|detail. The last occurrence wins, matching
// HotSpot flag processing. Sampling is gated on this so NMT is never enabled
// (or probed) implicitly on JVMs that did not opt in.
func NMTEnabledInArgs(vmArgs string) bool {
	const prefix = "-XX:NativeMemoryTracking="
	enabled := false
	for _, tok := range strings.Fields(vmArgs) {
		if v, ok := strings.CutPrefix(tok, prefix); ok {
			enabled = v == "summary" || v == "detail"
		}
	}
	return enabled
}

// CollectNMTCommitted runs a cost-bounded `jcmd <pid> VM.native_memory summary`
// and returns committed KiB per category. It returns ErrNMTDisabled when the
// target reports NMT is off and ErrNMTSampleTimeout when the call exceeds
// timeout (zero means DefaultNMTSampleTimeout).
func CollectNMTCommitted(pid int, run CmdRunner, timeout time.Duration) (map[string]int64, error) {
	if run == nil {
		run = DefaultRunner
	}
	if timeout <= 0 {
		timeout = DefaultNMTSampleTimeout
	}
	type result struct {
		out []byte
		err error
	}
	ch := make(chan result, 1) // buffered so a late finisher does not block forever
	go func() {
		out, err := NativeMemory(pid, "summary", run)
		ch <- result{out, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var res result
	select {
	case res = <-ch:
	case <-timer.C:
		return nil, fmt.Errorf("pid %d: %w after %s", pid, ErrNMTSampleTimeout, timeout)
	}
	if res.err != nil {
		return nil, res.err
	}
	b := ParseNMTSummary(string(res.out))
	if !b.Enabled {
		return nil, ErrNMTDisabled
	}
	if len(b.Categories) == 0 {
		return nil, nil
	}
	out := make(map[string]int64, len(b.Categories))
	for _, c := range b.Categories {
		out[c.Name] = c.CommittedKiB
	}
	return out, nil
}
