package pressure

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadPSI(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pressure/cpu", "some avg10=4.76 avg60=26.31 avg300=10.34 total=36510539\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	write(t, root, "pressure/memory", "some avg10=12.50 avg60=3.00 avg300=1.00 total=100\nfull avg10=2.25 avg60=0.50 avg300=0.10 total=50\n")
	write(t, root, "pressure/io", "some avg10=0.12 avg60=0.30 avg300=0.15 total=793942\nfull avg10=0.10 avg60=0.18 avg300=0.08 total=496093\n")
	p, err := ReadPSI(root)
	if err != nil {
		t.Fatal(err)
	}
	if p.CPU.Some.Avg60 != 26.31 || p.CPU.Some.TotalUsec != 36510539 {
		t.Fatalf("cpu = %+v", p.CPU)
	}
	if p.Memory.Full == nil || p.Memory.Full.Avg10 != 2.25 {
		t.Fatalf("memory full = %+v", p.Memory.Full)
	}
	kinds := map[string]Severity{}
	for _, f := range AssessPSI(p) {
		kinds[f.Kind] = f.Severity
	}
	if kinds["memory-pressure"] != SeverityWarning || kinds["memory-stall"] != SeverityWarning {
		t.Fatalf("findings = %v", kinds)
	}
	if len(kinds) != 2 {
		t.Fatalf("expected only memory findings (cpu some 4.76 < 10, io idle), got %v", kinds)
	}
}

func TestReadPSIUnavailable(t *testing.T) {
	_, err := ReadPSI(t.TempDir())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestReadPSIOldCPUHasNoFull(t *testing.T) {
	r, err := parseResource([]byte("some avg10=1.00 avg60=2.00 avg300=3.00 total=7\n"))
	if err != nil || r.Full != nil || r.Some.Avg300 != 3 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestParseResourceRejectsGarbage(t *testing.T) {
	if _, err := parseResource([]byte("some avg10=abc\n")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := parseResource([]byte("")); err == nil {
		t.Fatal("expected error for empty")
	}
}

func TestV2Path(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
		ok   bool
	}{
		"v2":      {"0::/system.slice/app.service\n", "/system.slice/app.service", true},
		"hybrid":  {"12:pids:/\n1:name=systemd:/x\n0::/user.slice\n", "/user.slice", true},
		"v1 only": {"4:memory:/a\n3:cpu:/b\n", "", false},
		"deleted": {"0::/gone (deleted)\n", "/gone", true},
	}
	for name, c := range cases {
		got, ok := v2Path([]byte(c.in))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %q,%v want %q,%v", name, got, ok, c.want, c.ok)
		}
	}
}

func cgFixture(t *testing.T) (proc, cg string) {
	proc, cg = t.TempDir(), t.TempDir()
	write(t, proc, "42/cgroup", "0::/svc.slice/app.service\n")
	write(t, cg, "cgroup.controllers", "cpu memory pids\n")
	// The limit lives on the parent service slice, not the leaf.
	write(t, cg, "svc.slice/memory.max", "1073741824\n")
	write(t, cg, "svc.slice/app.service/cgroup.controllers", "cpu memory pids\n")
	write(t, cg, "svc.slice/app.service/memory.max", "max\n")
	write(t, cg, "svc.slice/app.service/memory.high", "max\n")
	write(t, cg, "svc.slice/app.service/memory.current", "1048576000\n")
	write(t, cg, "svc.slice/app.service/memory.swap.current", "0\n")
	write(t, cg, "svc.slice/app.service/memory.stat", "anon 1000000000\nfile 48576000\n")
	write(t, cg, "svc.slice/app.service/memory.events", "low 0\nhigh 0\nmax 12\noom 3\noom_kill 2\n")
	write(t, cg, "svc.slice/app.service/memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	write(t, cg, "svc.slice/app.service/cpu.stat", "usage_usec 5000000\nuser_usec 4000000\nsystem_usec 1000000\nnr_periods 100\nnr_throttled 40\nthrottled_usec 2500000\n")
	write(t, cg, "svc.slice/app.service/cpu.max", "50000 100000\n")
	write(t, cg, "svc.slice/app.service/pids.current", "95\n")
	write(t, cg, "svc.slice/app.service/pids.max", "100\n")
	return proc, cg
}

func TestReadCgroupAndAssess(t *testing.T) {
	proc, root := cgFixture(t)
	cg, err := ReadCgroup(proc, root, 42)
	if err != nil {
		t.Fatal(err)
	}
	m := cg.Memory
	if m == nil || m.Max != Unlimited || m.EffectiveMax != 1073741824 || m.LimitedBy != "/svc.slice" {
		t.Fatalf("memory = %+v", m)
	}
	if m.Events.OOMKill != 2 || m.Anon != 1000000000 {
		t.Fatalf("memory events/anon = %+v", m)
	}
	if cg.CPU == nil || cg.CPU.QuotaUsec != 50000 || cg.CPU.PeriodUsec != 100000 || cg.CPU.NrThrottled != 40 {
		t.Fatalf("cpu = %+v", cg.CPU)
	}
	got := map[string]Severity{}
	var sum []string
	for _, f := range AssessCgroup(cg) {
		got[f.Kind] = f.Severity
		sum = append(sum, f.Summary)
	}
	for kind, sev := range map[string]Severity{
		"cgroup-oom-kill":          SeverityCritical,
		"cgroup-memory-near-limit": SeverityCritical,
		"cpu-throttled":            SeverityWarning,
		"pids-near-limit":          SeverityCritical,
	} {
		if got[kind] != sev {
			t.Errorf("%s = %q, want %q (all: %v)", kind, got[kind], sev, got)
		}
	}
	if _, dup := got["cgroup-memory-max-hit"]; dup {
		t.Error("max-hit must be suppressed once a kill is reported")
	}
	if !strings.Contains(strings.Join(sum, "\n"), "limit set on /svc.slice") {
		t.Errorf("near-limit summary should name the limiting ancestor: %v", sum)
	}
}

func TestReadCgroupV1Unavailable(t *testing.T) {
	proc, root := t.TempDir(), t.TempDir()
	write(t, proc, "7/cgroup", "4:memory:/a\n")
	if _, err := ReadCgroup(proc, root, 7); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	write(t, proc, "8/cgroup", "0::/x\n")
	if _, err := ReadCgroup(proc, root, 8); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("hybrid with no controllers: err = %v", err)
	}
}

func TestAssessHealthyCgroup(t *testing.T) {
	cg := Cgroup{Path: "/a", Memory: &Memory{Current: 100, Anon: 50, EffectiveMax: 1 << 30, Max: Unlimited, High: Unlimited}}
	if f := AssessCgroup(cg); len(f) != 0 {
		t.Fatalf("findings = %v", f)
	}
}
