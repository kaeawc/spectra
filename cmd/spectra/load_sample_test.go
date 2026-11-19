package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func sampledLoadDeps(now *time.Time) loadDeps {
	deps := fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n")
	stat := func(pid, cpu int) string {
		return fmt.Sprintf("%d (w) S 1 1 1 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 5 0 0\n", pid, cpu)
	}
	inner := deps.Host.ReadFile
	cpu := 100
	deps.Host.ReadFile = func(p string) ([]byte, error) {
		switch p {
		case "/proc/10/stat":
			return []byte(stat(10, cpu)), nil
		case "/proc/11/stat":
			return []byte(stat(11, 9000)), nil
		}
		return inner(p)
	}
	deps.Now = func() time.Time { return *now }
	deps.Sleep = func(_ context.Context, d time.Duration) { cpu += 25; *now = now.Add(d) }
	return deps
}

func TestRunLoadSampleWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	var out, errb bytes.Buffer
	if code := runLoadWithIO([]string{"--json"}, &out, &errb, sampledLoadDeps(&now)); code != 0 {
		t.Fatalf("exit = %d; %s", code, errb.String())
	}
	var m struct {
		Window int64 `json:"cpu_window_ms"`
		TopCPU []struct {
			PID    int     `json:"pid"`
			CPUPct float64 `json:"cpu_pct"`
		} `json:"top_cpu"`
	}
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Window != 500 || m.TopCPU[0].PID != 10 || m.TopCPU[0].CPUPct != 50 {
		t.Fatalf("window=%d top=%+v", m.Window, m.TopCPU)
	}

	now = time.Unix(1000, 0)
	out.Reset()
	if code := runLoadWithIO(nil, &out, &errb, sampledLoadDeps(&now)); code != 0 || !strings.Contains(out.String(), "recent, over 500 ms") {
		t.Fatalf("text lacks window (exit %d):\n%s", code, out.String())
	}

	now = time.Unix(1000, 0)
	out.Reset()
	if code := runLoadWithIO([]string{"--json", "--sample", "0"}, &out, &errb, sampledLoadDeps(&now)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Window != 0 || m.TopCPU[0].CPUPct != 90 {
		t.Fatalf("--sample 0 should keep lifetime values: window=%d top=%+v", m.Window, m.TopCPU)
	}

	out.Reset()
	if code := runLoadWithIO(nil, &out, &errb, fakeLoadDeps("1 1 1 1/1 1", "MemAvailable: 16777216 kB\n")); code != 0 || !strings.Contains(out.String(), "lifetime average") {
		t.Fatalf("text should say lifetime:\n%s", out.String())
	}
	if code := runLoadWithIO([]string{"--sample", "-1s"}, &out, &errb, fakeLoadDeps("1 1 1 1/1 1", "")); code != 2 {
		t.Fatalf("negative sample exit = %d", code)
	}
}
