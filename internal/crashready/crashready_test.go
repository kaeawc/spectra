package crashready

import (
	"strings"
	"testing"
)

// fakeHost is a scripted Host for table tests.
type fakeHost struct {
	sysctls    map[string]string
	sysctlErr  map[string]error
	softLimit  uint64
	hardLimit  uint64
	limitErr   error
	coresDir   bool
	dialogType string
}

func (f fakeHost) Sysctl(key string) (string, error) {
	if f.sysctlErr != nil {
		if err := f.sysctlErr[key]; err != nil {
			return "", err
		}
	}
	return f.sysctls[key], nil
}
func (f fakeHost) CoreRLimit() (uint64, uint64, error) { return f.softLimit, f.hardLimit, f.limitErr }
func (f fakeHost) CoresDir() bool                      { return f.coresDir }
func (f fakeHost) CrashReporterDialogType() string     { return f.dialogType }

func statusOf(r Report, name string) Status {
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	return Status("<missing>")
}

func TestCoredumpDisabledWarns(t *testing.T) {
	h := fakeHost{sysctls: map[string]string{"kern.coredump": "0"}, softLimit: rlimInfinity, hardLimit: rlimInfinity, coresDir: true}
	r := Evaluate(h, nil)
	if got := statusOf(r, "kern.coredump"); got != StatusWarn {
		t.Fatalf("kern.coredump status = %q, want warn", got)
	}
	if !r.Ready() {
		t.Error("disabled cores are advisory (warn), report should still be Ready")
	}
}

func TestCoredumpEnabledOK(t *testing.T) {
	h := fakeHost{
		sysctls:   map[string]string{"kern.coredump": "1", "kern.corefile": "/cores/core.%P"},
		softLimit: rlimInfinity, hardLimit: rlimInfinity, coresDir: true,
	}
	r := Evaluate(h, nil)
	if got := statusOf(r, "kern.coredump"); got != StatusOK {
		t.Fatalf("kern.coredump status = %q, want ok", got)
	}
	var detail string
	for _, c := range r.Checks {
		if c.Name == "kern.coredump" {
			detail = c.Detail
		}
	}
	if want := "/cores/core.%P"; !strings.Contains(detail, want) {
		t.Errorf("detail %q should mention corefile pattern %q", detail, want)
	}
}

func TestCoreLimitZeroWarns(t *testing.T) {
	h := fakeHost{sysctls: map[string]string{"kern.coredump": "1"}, softLimit: 0, hardLimit: rlimInfinity, coresDir: true}
	r := Evaluate(h, nil)
	if got := statusOf(r, "core size limit"); got != StatusWarn {
		t.Fatalf("core size limit status = %q, want warn", got)
	}
}

func TestCoresDirMissingWhileEnabledWarns(t *testing.T) {
	h := fakeHost{sysctls: map[string]string{"kern.coredump": "1"}, softLimit: rlimInfinity, coresDir: false}
	if got := statusOf(Evaluate(h, nil), "/cores"); got != StatusWarn {
		t.Fatalf("/cores status = %q, want warn (enabled but absent)", got)
	}
}

func TestCoresDirMissingWhileDisabledOK(t *testing.T) {
	h := fakeHost{sysctls: map[string]string{"kern.coredump": "0"}, softLimit: rlimInfinity, coresDir: false}
	if got := statusOf(Evaluate(h, nil), "/cores"); got != StatusOK {
		t.Fatalf("/cores status = %q, want ok (moot while disabled)", got)
	}
}

func TestAppHardenedWithoutGetTaskAllowWarns(t *testing.T) {
	h := fakeHost{sysctls: map[string]string{"kern.coredump": "1"}, softLimit: rlimInfinity, coresDir: true}
	app := &AppDebug{Name: "Locked", HardenedRuntime: true, GetTaskAllow: false}
	r := Evaluate(h, app)
	if r.App != "Locked" {
		t.Errorf("report App = %q, want Locked", r.App)
	}
	if got := statusOf(r, "app: Locked"); got != StatusWarn {
		t.Fatalf("app status = %q, want warn", got)
	}
}

func TestAppDebuggableOK(t *testing.T) {
	h := fakeHost{sysctls: map[string]string{"kern.coredump": "1"}, softLimit: rlimInfinity, coresDir: true}
	app := &AppDebug{Name: "Debuggable", HardenedRuntime: true, GetTaskAllow: true}
	if got := statusOf(Evaluate(h, app), "app: Debuggable"); got != StatusOK {
		t.Fatalf("app status = %q, want ok", got)
	}
}

func TestReadyAndCounts(t *testing.T) {
	// cores enabled but /cores absent -> warn; nothing critical -> Ready.
	h := fakeHost{sysctls: map[string]string{"kern.coredump": "1"}, softLimit: 0, coresDir: false}
	r := Evaluate(h, nil)
	warn, crit := r.Counts()
	if crit != 0 {
		t.Errorf("critical = %d, want 0", crit)
	}
	if warn < 2 {
		t.Errorf("warn = %d, want >= 2 (rlimit 0 and /cores absent)", warn)
	}
	if !r.Ready() {
		t.Error("Ready() should be true with no critical checks")
	}
}

func TestLimitString(t *testing.T) {
	if got := limitString(rlimInfinity); got != "unlimited" {
		t.Errorf("limitString(inf) = %q, want unlimited", got)
	}
	if got := limitString(0); got != "0" {
		t.Errorf("limitString(0) = %q, want 0", got)
	}
}
