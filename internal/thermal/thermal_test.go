package thermal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func pmsetRunner(out string) proc.Runner {
	return proc.NewFake().OnExact("pmset", []string{"-g", "therm"}, proc.Response{Result: proc.Result{Stdout: []byte(out)}})
}

func TestDarwinPmset(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		verdict  Verdict
		evidence string
	}{
		{"limits at 100", "CPU Power notify\n  CPU_Scheduler_Limit\t= 100\n  CPU_Available_CPUs\t= 8\n  CPU_Speed_Limit\t= 100\n", Nominal, "no CPU speed limit"},
		{"speed limit 62", "  CPU_Scheduler_Limit\t= 100\n  CPU_Available_CPUs\t= 8\n  CPU_Speed_Limit \t= 62\n", Throttled, "CPU_Speed_Limit 62"},
		{"scheduler limit", "  CPU_Scheduler_Limit\t= 70\n  CPU_Speed_Limit \t= 100\n", Throttled, "CPU_Scheduler_Limit 70"},
		{"performance warning", "Thermal warning level: 0\nPerformance warning level: 2\n", Throttled, "performance warning level 2"},
		{"thermal warning", "Thermal warning level: 1\nNote: No performance warning level has been recorded\n", Warm, "thermal warning level 1"},
		{"apple silicon none recorded", "Note: No thermal warning level has been recorded\nNote: No performance warning level has been recorded\nNote: No CPU power status has been recorded\n", Nominal, "no CPU speed limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Collector{OS: hostos.Darwin, Runner: pmsetRunner(tc.out)}
			r, err := c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if r.Verdict != tc.verdict || !strings.Contains(r.Evidence, tc.evidence) {
				t.Fatalf("got %s %q, want %s %q", r.Verdict, r.Evidence, tc.verdict, tc.evidence)
			}
		})
	}
}

func TestDarwinMissingFieldsStayNil(t *testing.T) {
	c := &Collector{OS: hostos.Darwin, Runner: pmsetRunner("Note: No thermal warning level has been recorded\nNote: No performance warning level has been recorded\n")}
	r, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l := r.Limits
	if l.SpeedLimit != nil || l.SchedulerLimit != nil || l.AvailableCPUs != nil || l.ThermalWarning != nil || l.PerformanceWarning != nil {
		t.Fatalf("expected all nil, got %+v", l)
	}
	if len(r.Notes) == 0 {
		t.Error("expected a note about missing limit fields")
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "cpu_speed_limit") {
		t.Errorf("nil limit leaked into JSON: %s", b)
	}
}

func TestDarwinRunnerError(t *testing.T) {
	c := &Collector{OS: hostos.Darwin, Runner: proc.NewFake()}
	if _, err := c.Collect(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func zoneFS(temp int, trips map[string]string, extra fstest.MapFS) fstest.MapFS {
	m := fstest.MapFS{
		"sys/class/thermal/thermal_zone0/type": {Data: []byte("x86_pkg_temp\n")},
		"sys/class/thermal/thermal_zone0/temp": {Data: []byte(fmt.Sprintf("%d\n", temp))},
	}
	i := 0
	for typ, tmp := range trips {
		m[fmt.Sprintf("sys/class/thermal/thermal_zone0/trip_point_%d_temp", i)] = &fstest.MapFile{Data: []byte(tmp + "\n")}
		m[fmt.Sprintf("sys/class/thermal/thermal_zone0/trip_point_%d_type", i)] = &fstest.MapFile{Data: []byte(typ + "\n")}
		i++
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestLinux(t *testing.T) {
	cooling := func(typ string, cur int) fstest.MapFS {
		return fstest.MapFS{
			"sys/class/thermal/thermal_zone0/temp":        {Data: []byte("40000")},
			"sys/class/thermal/cooling_device3/type":      {Data: []byte(typ + "\n")},
			"sys/class/thermal/cooling_device3/cur_state": {Data: []byte(fmt.Sprintf("%d\n", cur))},
			"sys/class/thermal/cooling_device3/max_state": {Data: []byte("10\n")},
		}
	}
	cases := []struct {
		name     string
		fs       fstest.MapFS
		verdict  Verdict
		evidence string
	}{
		{"zone above passive trip", zoneFS(96000, map[string]string{"passive": "95000"}, nil), Throttled, "zone x86_pkg_temp 96°C ≥ passive trip 95°C"},
		{"zone within margin", zoneFS(91000, map[string]string{"passive": "95000"}, nil), Warm, "within 5°C of passive trip 95°C"},
		{"zone far below", zoneFS(60000, map[string]string{"passive": "95000", "critical": "105000"}, nil), Nominal, "no thermal zone"},
		{"zone at active trip", zoneFS(70000, map[string]string{"active": "65000"}, nil), Warm, "active trip 65°C"},
		{"processor cooling", cooling("Processor", 2), Throttled, "cooling_device3 (Processor) cur_state 2/10"},
		{"powerclamp cooling", cooling("intel_powerclamp", 1), Throttled, "intel_powerclamp"},
		{"fan at max", cooling("Fan", 10), Warm, "cooling_device3 (Fan) cur_state 10/10"},
		{"idle fan 1/10", cooling("Fan", 1), Nominal, "no thermal zone"},
		{"TCC offset", cooling("TCC Offset", 63), Nominal, "no thermal zone"},
		{"placeholder trip 0", zoneFS(45000, map[string]string{"passive": "0"}, nil), Nominal, "no thermal zone"},
		{"placeholder trip -274000", zoneFS(45000, map[string]string{"critical": "-274000"}, nil), Nominal, "no thermal zone"},
		{"disabled zone", zoneFS(99000, map[string]string{"passive": "95000"}, fstest.MapFS{
			"sys/class/thermal/thermal_zone0/mode": {Data: []byte("disabled\n")}}), Nominal, "no thermal zone"},
		{"zone temp zero", zoneFS(0, map[string]string{"passive": "95000"}, nil), Nominal, "no thermal zone"},
		{"cap with hot zone", zoneFS(96000, map[string]string{"passive": "95000"}, fstest.MapFS{
			"sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq": {Data: []byte("1200000\n")},
			"sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq": {Data: []byte("3600000\n")}}), Throttled, "zone x86_pkg_temp 96"},
		{"idle processor cooling", cooling("Processor", 0), Nominal, "no thermal zone"},
		{"cpufreq capped, cool zone", fstest.MapFS{
			"sys/class/thermal/thermal_zone0/temp":                 {Data: []byte("40000")},
			"sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq": {Data: []byte("1200000\n")},
			"sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq": {Data: []byte("3600000\n")},
		}, Nominal, "no thermal zone"},
		{"cpufreq near max", fstest.MapFS{
			"sys/class/thermal/thermal_zone0/temp":                 {Data: []byte("40000")},
			"sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq": {Data: []byte("3500000\n")},
			"sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq": {Data: []byte("3600000\n")},
		}, Nominal, "no thermal zone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Collector{OS: hostos.Linux, FS: tc.fs}
			r, err := c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if r.Verdict != tc.verdict || !strings.Contains(r.Evidence, tc.evidence) {
				t.Fatalf("got %s %q, want %s %q", r.Verdict, r.Evidence, tc.verdict, tc.evidence)
			}
		})
	}
}

func TestLinuxNotes(t *testing.T) {
	capFS := func(extra fstest.MapFS) fstest.MapFS {
		m := fstest.MapFS{
			"sys/class/thermal/thermal_zone0/temp":                 {Data: []byte("40000")},
			"sys/class/thermal/cooling_device0/type":               {Data: []byte("Fan")},
			"sys/class/thermal/cooling_device0/cur_state":          {Data: []byte("1")},
			"sys/class/thermal/cooling_device0/max_state":          {Data: []byte("10")},
			"sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq": {Data: []byte("1200000\n")},
			"sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq": {Data: []byte("3600000\n")},
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name string
		fs   fstest.MapFS
		want []string
		not  string
	}{
		{"cap and idle fan", capFS(nil), []string{"frequency capped at 33%", "may be a power profile", "cooling_device0 (Fan) cur_state 1/10"}, "no_turbo"},
		{"no_turbo", capFS(fstest.MapFS{"sys/devices/system/cpu/intel_pstate/no_turbo": {Data: []byte("1\n")}}), []string{"no_turbo is 1"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := (&Collector{OS: hostos.Linux, FS: tc.fs}).Collect(context.Background())
			if err != nil || r.Verdict != Nominal {
				t.Fatalf("err=%v verdict=%s", err, r.Verdict)
			}
			all := strings.Join(r.Notes, "\n")
			for _, w := range tc.want {
				if !strings.Contains(all, w) {
					t.Errorf("notes missing %q:\n%s", w, all)
				}
			}
			if tc.not != "" && strings.Contains(all, tc.not) {
				t.Errorf("notes unexpectedly contain %q:\n%s", tc.not, all)
			}
		})
	}
}

func TestLinuxMissingThermalDirIsNominal(t *testing.T) {
	c := &Collector{OS: hostos.Linux, FS: fstest.MapFS{}}
	r, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != Nominal || len(r.Notes) == 0 || !strings.Contains(r.Notes[0], "/sys/class/thermal") {
		t.Fatalf("got %+v", r)
	}
}

func TestUnknownOSUnsupported(t *testing.T) {
	defer hostos.SetForTest(hostos.Unknown)()
	if _, err := (&Collector{}).Collect(context.Background()); err == nil {
		t.Fatal("expected unsupported error")
	}
}

func counterFS(n int) fstest.MapFS {
	return fstest.MapFS{
		"sys/class/thermal/thermal_zone0/temp":                                {Data: []byte("40000")},
		"sys/devices/system/cpu/cpu0/thermal_throttle/package_throttle_count": {Data: []byte(fmt.Sprintf("%d\n", n))},
	}
}

func TestWatchCounterIncreaseIsThrottled(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	sleeps := 0
	c := &Collector{OS: hostos.Linux, FS: counterFS(3), Clock: clk}
	c.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps++
		clk.Advance(d)
		if sleeps == 2 {
			c.FS = counterFS(5)
		}
		return nil
	}
	w, err := c.Watch(context.Background(), 6*time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if sleeps != 3 || w.Samples != 4 {
		t.Fatalf("sleeps=%d samples=%d", sleeps, w.Samples)
	}
	if w.Verdict != Throttled || len(w.CounterIncreases) != 1 || w.CounterIncreases[0].From != 3 || w.CounterIncreases[0].To != 5 {
		t.Fatalf("got %+v", w)
	}
	if !strings.Contains(w.Evidence, "package_throttle_count rose 3 -> 5") {
		t.Errorf("evidence %q", w.Evidence)
	}
}

func TestWatchKeepsIncreaseWhenCounterBecomesUnreadable(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	sleeps := 0
	c := &Collector{OS: hostos.Linux, FS: counterFS(3), Clock: clk}
	c.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps++
		clk.Advance(d)
		switch sleeps {
		case 1:
			c.FS = counterFS(5)
		case 2:
			c.FS = fstest.MapFS{"sys/class/thermal/thermal_zone0/temp": {Data: []byte("40000")}}
		}
		return nil
	}
	w, err := c.Watch(context.Background(), 4*time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if w.Verdict != Throttled || len(w.CounterIncreases) != 1 || w.CounterIncreases[0].From != 3 || w.CounterIncreases[0].To != 5 {
		t.Fatalf("got %s %+v", w.Verdict, w.CounterIncreases)
	}
}

func TestCoolCappedCPUEvidenceDoesNotDenyCap(t *testing.T) {
	fsys := fstest.MapFS{
		"sys/class/thermal/thermal_zone0/temp":                 {Data: []byte("40000")},
		"sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq": {Data: []byte("1200000")},
		"sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq": {Data: []byte("3600000")},
	}
	r, _ := (&Collector{OS: hostos.Linux, FS: fsys}).Collect(context.Background())
	if r.Verdict != Nominal || len(r.FreqCaps) != 1 || strings.Contains(r.Evidence, "no CPU frequency cap") {
		t.Fatalf("got %s %q %+v", r.Verdict, r.Evidence, r.FreqCaps)
	}
}

func TestWatchReportsWorstVerdictAndSteadyCounter(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	c := &Collector{OS: hostos.Linux, FS: counterFS(3), Clock: clk}
	n := 0
	c.Sleep = func(_ context.Context, d time.Duration) error {
		clk.Advance(d)
		n++
		if n == 1 {
			c.FS = zoneFS(91000, map[string]string{"passive": "95000"}, fstest.MapFS{
				"sys/devices/system/cpu/cpu0/thermal_throttle/package_throttle_count": {Data: []byte("3\n")},
			})
		} else {
			c.FS = counterFS(3)
		}
		return nil
	}
	w, err := c.Watch(context.Background(), 4*time.Second, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if w.Verdict != Warm || len(w.CounterIncreases) != 0 {
		t.Fatalf("got %s %+v", w.Verdict, w.CounterIncreases)
	}
}

func TestWatchCapsSleepToRemainingDuration(t *testing.T) {
	cases := []struct {
		name               string
		duration, interval time.Duration
		samples            int
	}{
		{"interval larger than duration", time.Second, time.Hour, 2},
		{"interval does not divide duration", 5 * time.Second, 2 * time.Second, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := clock.NewFake(time.Unix(0, 0))
			var slept time.Duration
			c := &Collector{OS: hostos.Linux, FS: counterFS(3), Clock: clk}
			c.Sleep = func(_ context.Context, d time.Duration) error {
				slept += d
				clk.Advance(d)
				return nil
			}
			w, err := c.Watch(context.Background(), tc.duration, tc.interval)
			if err != nil {
				t.Fatal(err)
			}
			if slept > tc.duration || w.Samples != tc.samples {
				t.Fatalf("slept=%s samples=%d", slept, w.Samples)
			}
		})
	}
}

func TestWatchRejectsBadInterval(t *testing.T) {
	c := &Collector{OS: hostos.Linux, FS: fstest.MapFS{}}
	if _, err := c.Watch(context.Background(), time.Second, 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestWatchStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Collector{OS: hostos.Linux, FS: fstest.MapFS{}, Clock: clock.NewFake(time.Unix(0, 0))}
	c.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	w, err := c.Watch(ctx, time.Hour, time.Second)
	if err != nil || w.Samples != 1 {
		t.Fatalf("err=%v samples=%d", err, w.Samples)
	}
}
