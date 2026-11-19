package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/thermal"
)

func pmsetCollector(out string) *thermal.Collector {
	return &thermal.Collector{OS: hostos.Darwin, Runner: proc.NewFake().OnExact("pmset", []string{"-g", "therm"}, proc.Response{Result: proc.Result{Stdout: []byte(out)}})}
}

func linuxZone(temp string) *thermal.Collector {
	return &thermal.Collector{OS: hostos.Linux, FS: fstest.MapFS{
		"sys/class/thermal/thermal_zone0/type":              {Data: []byte("x86_pkg_temp")},
		"sys/class/thermal/thermal_zone0/temp":              {Data: []byte(temp)},
		"sys/class/thermal/thermal_zone0/trip_point_0_temp": {Data: []byte("95000")},
		"sys/class/thermal/thermal_zone0/trip_point_0_type": {Data: []byte("passive")},
	}}
}

func TestThermalExitCodes(t *testing.T) {
	cases := []struct {
		name string
		c    *thermal.Collector
		want int
		text string
	}{
		{"darwin nominal", pmsetCollector("CPU_Speed_Limit = 100\n"), 0, "thermal: nominal"},
		{"darwin warm", pmsetCollector("Thermal warning level: 1\n"), 3, "thermal: warm"},
		{"darwin throttled", pmsetCollector("CPU_Speed_Limit = 62\n"), 4, "CPU_Speed_Limit 62"},
		{"linux nominal", linuxZone("50000"), 0, "thermal: nominal"},
		{"linux warm", linuxZone("92000"), 3, "thermal: warm"},
		{"linux throttled", linuxZone("96000"), 4, "zone x86_pkg_temp 96°C ≥ passive trip 95°C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if got := runThermalWithIO(context.Background(), nil, &out, &errb, tc.c); got != tc.want {
				t.Fatalf("exit = %d, want %d (stderr=%s)", got, tc.want, errb.String())
			}
			if !strings.Contains(out.String(), tc.text) {
				t.Errorf("output missing %q:\n%s", tc.text, out.String())
			}
		})
	}
}

func TestThermalCLIPerOS(t *testing.T) {
	for _, k := range []hostos.Kind{hostos.Linux, hostos.Darwin} {
		t.Run(k.String(), func(t *testing.T) {
			defer hostos.SetForTest(k)()
			c := &thermal.Collector{FS: fstest.MapFS{}, Runner: proc.NewFake().OnExact("pmset", []string{"-g", "therm"}, proc.Response{Result: proc.Result{Stdout: []byte("CPU_Speed_Limit = 100\n")}})}
			var out, errb bytes.Buffer
			if code := runThermalWithIO(context.Background(), []string{"--json"}, &out, &errb, c); code != 0 {
				t.Fatalf("exit = %d, stderr=%s", code, errb.String())
			}
			var got struct {
				OS       string `json:"os"`
				Verdict  string `json:"verdict"`
				Evidence string `json:"evidence"`
				Samples  int    `json:"samples"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("json: %v\n%s", err, out.String())
			}
			if got.OS != k.String() || got.Verdict != "nominal" || got.Evidence == "" || got.Samples != 1 {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestThermalWatchFlags(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	c := linuxZone("96000")
	c.Clock = clk
	var intervals []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		intervals = append(intervals, d)
		clk.Advance(d)
		return nil
	}
	var out, errb bytes.Buffer
	if code := runThermalWithIO(context.Background(), []string{"--watch", "4s", "--interval", "2s"}, &out, &errb, c); code != 4 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	if len(intervals) != 2 || intervals[0] != 2*time.Second {
		t.Errorf("intervals = %v", intervals)
	}
	if !strings.Contains(out.String(), "worst of 3 samples") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestThermalUsageAndErrorExits(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"extra"}, {"--interval", "0s"}, {"--watch", "-1s"}} {
		var out, errb bytes.Buffer
		if code := runThermalWithIO(context.Background(), args, &out, &errb, linuxZone("1000")); code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
	}
	var out, errb bytes.Buffer
	c := &thermal.Collector{OS: hostos.Darwin, Runner: proc.NewFake()}
	if code := runThermalWithIO(context.Background(), nil, &out, &errb, c); code != 1 {
		t.Errorf("runner error: exit = %d, want 1", code)
	}
}

func TestThermalTextWriteErrorExitsOne(t *testing.T) {
	var stderr bytes.Buffer
	code := runThermalWithIO(context.Background(), nil, failWriter{}, &stderr, linuxZone("40000"))
	if code != 1 || !strings.Contains(stderr.String(), "thermal: write output: closed") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
