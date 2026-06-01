package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/memstate"
)

func runMemory(args []string) int {
	fs := flag.NewFlagSet("spectra memory", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	watch := fs.String("watch", "", "Repeat every interval (for example: 1, 1s, 5s)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	interval, err := parseMemoryWatch(*watch)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if interval > 0 {
		return runMemoryWatch(interval, *asJSON)
	}
	state, err := memstate.Collect()
	if err != nil {
		return printMemoryErr(err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}
	printMemoryState(state, memstate.MemoryState{})
	return 0
}

func runMemoryWatch(interval time.Duration, asJSON bool) int {
	t := time.NewTicker(interval)
	defer t.Stop()
	var prev memstate.MemoryState
	for {
		state, err := memstate.Collect()
		if err != nil {
			return printMemoryErr(err)
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(state)
		} else {
			printMemoryState(state, prev)
		}
		prev = state
		<-t.C
	}
}

func parseMemoryWatch(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, fmt.Errorf("--watch must be positive")
		}
		return time.Duration(seconds) * time.Second, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --watch: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--watch must be positive")
	}
	return d, nil
}

func printMemoryErr(err error) int {
	if errors.Is(err, memstate.ErrNotSupported) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}

func printMemoryState(s, prev memstate.MemoryState) {
	parts := []string{
		"phys=" + memoryBytes(s.PhysicalBytes),
		"wired=" + memoryBytes(s.Wired),
		"app=" + memoryBytes(s.Anonymous),
		fmt.Sprintf("compressed=%s(stored=%s ratio=%.1fx)",
			memoryBytes(s.CompressorOccupied),
			memoryBytes(s.CompressorStored),
			s.CompressorRatio,
		),
		"swap=" + memoryBytes(s.Swap.UsedBytes),
		"pressure=" + string(s.PressureLevel),
		fmt.Sprintf("free=%.0f%%", s.PressureFreePercent),
	}
	if !prev.CollectedAt.IsZero() {
		elapsed := s.CollectedAt.Sub(prev.CollectedAt).Seconds()
		if elapsed > 0 {
			parts = append(parts,
				fmt.Sprintf("compressions/s=%.0f", rate(s.Compressions, prev.Compressions, elapsed)),
				fmt.Sprintf("swapouts/s=%.0f", rate(s.SwapOuts, prev.SwapOuts, elapsed)),
			)
		}
	}
	fmt.Println(strings.Join(parts, "  "))
}

func rate(current, previous uint64, seconds float64) float64 {
	if current < previous {
		return 0
	}
	return float64(current-previous) / seconds
}

func memoryBytes(n uint64) string {
	const k uint64 = 1024
	switch {
	case n >= k*k*k:
		return fmt.Sprintf("%.1fGB", float64(n)/float64(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.0fMB", float64(n)/float64(k*k))
	case n >= k:
		return fmt.Sprintf("%.0fKB", float64(n)/float64(k))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
