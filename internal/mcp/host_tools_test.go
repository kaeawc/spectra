package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/playbook"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/syslimits"
	"github.com/kaeawc/spectra/internal/timemachine"
	"github.com/kaeawc/spectra/internal/updates"
)

// --- fakes -------------------------------------------------------------------

type fakePowerCollector struct {
	state      sysinfo.PowerState
	assertions []sysinfo.PowerAssertion
	deltas     []sysinfo.EnergyDelta
	err        error
	sawPids    []int
}

func (f *fakePowerCollector) CollectPower() sysinfo.PowerState { return f.state }
func (f *fakePowerCollector) CollectAssertions() []sysinfo.PowerAssertion {
	return f.assertions
}
func (f *fakePowerCollector) SampleEnergy(_ context.Context, _ time.Duration, pids []int) ([]sysinfo.EnergyDelta, error) {
	f.sawPids = append([]int{}, pids...)
	return f.deltas, f.err
}

type fakeMemoryCollector struct {
	state memstate.MemoryState
	err   error
}

func (f fakeMemoryCollector) Collect() (memstate.MemoryState, error) { return f.state, f.err }

type fakeStorageCollector struct {
	state   storagestate.State
	sawOpts storagestate.CollectOptions
}

func (f *fakeStorageCollector) Collect(opts storagestate.CollectOptions) storagestate.State {
	f.sawOpts = opts
	return f.state
}

type fakeSystemCollector struct {
	limits   syslimits.SystemLimits
	holders  syslimits.TopHolders
	sawLimit int
}

func (f *fakeSystemCollector) Collect(_ syslimits.Options) syslimits.SystemLimits { return f.limits }
func (f *fakeSystemCollector) CollectTopHolders(_ context.Context, limit int, _ process.CollectOptions) syslimits.TopHolders {
	f.sawLimit = limit
	return f.holders
}

type fakeServicesCollector struct {
	inv services.LaunchInventory
	err error
}

func (f fakeServicesCollector) List(_ context.Context, _ services.Options) (services.LaunchInventory, error) {
	return f.inv, f.err
}

type fakeLogCollector struct {
	result logquery.Result
	err    error
	sawQ   logquery.Query
}

func (f *fakeLogCollector) Run(_ context.Context, q logquery.Query) (logquery.Result, error) {
	f.sawQ = q
	return f.result, f.err
}

type fakeUpdatesCollector struct {
	history updates.InstallHistory
	log     updates.Result
	err     error
}

func (f fakeUpdatesCollector) CollectHistory(_ context.Context) (updates.InstallHistory, error) {
	return f.history, f.err
}
func (f fakeUpdatesCollector) QueryInstallLog(_ updates.Query) (updates.Result, error) {
	return f.log, f.err
}

type fakeTimeMachineCollector struct {
	state timemachine.TimeMachineState
	err   error
}

func (f fakeTimeMachineCollector) Collect(_ context.Context) (timemachine.TimeMachineState, error) {
	return f.state, f.err
}

type fakePlaybookCatalog struct {
	list []playbook.Playbook
}

func (f fakePlaybookCatalog) List() []playbook.Playbook { return f.list }
func (f fakePlaybookCatalog) Get(id string) (playbook.Playbook, bool) {
	for _, pb := range f.list {
		if pb.ID == id {
			return pb, true
		}
	}
	return playbook.Playbook{}, false
}

func newHostServer(t *testing.T, c Collectors) *Server {
	t.Helper()
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	if c.Clock == nil {
		c.Clock = fixedClock{t: time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)}
	}
	s.SetCollectors(c)
	return s
}

// --- happy paths -------------------------------------------------------------

func TestPowerStateUsesInjectedCollector(t *testing.T) {
	s := newHostServer(t, Collectors{Power: &fakePowerCollector{}})
	res := s.toolPower(json.RawMessage(`{"operation":"state"}`))
	if res.IsError {
		t.Fatalf("power state errored: %+v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "battery and thermal state") {
		t.Fatalf("unexpected power state summary: %s", res.Content[0].Text)
	}
}

func TestPowerImpactScoresSampledProcesses(t *testing.T) {
	power := &fakePowerCollector{deltas: []sysinfo.EnergyDelta{
		{PID: 11, Command: "hot", EnergyNJ: 5_000_000, InterruptWakeups: 200},
		{PID: 22, Command: "cool", EnergyNJ: 1000},
	}}
	s := newHostServer(t, Collectors{Power: power})
	res := s.toolPower(json.RawMessage(`{"operation":"impact","pids":[11,22],"limit":5}`))
	if res.IsError {
		t.Fatalf("power impact errored: %+v", res.Content)
	}
	if got := power.sawPids; len(got) != 2 || got[0] != 11 || got[1] != 22 {
		t.Fatalf("energy sampler saw unexpected pids: %v", got)
	}
	if !strings.Contains(res.Content[0].Text, "ranked 2 process(es)") {
		t.Fatalf("unexpected impact summary: %s", res.Content[0].Text)
	}
}

func TestMemoryReportsCollectorError(t *testing.T) {
	s := newHostServer(t, Collectors{Memory: fakeMemoryCollector{err: errors.New("vm_stat failed")}})
	res := s.toolMemory(json.RawMessage(`{}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "vm_stat failed") {
		t.Fatalf("expected memory collector error, got %+v", res)
	}
}

func TestStoragePassesLimitAndPaths(t *testing.T) {
	storage := &fakeStorageCollector{}
	s := newHostServer(t, Collectors{Storage: storage})
	res := s.toolStorage(json.RawMessage(`{"paths":["/Applications/Slack.app"],"limit":3}`))
	if res.IsError {
		t.Fatalf("storage errored: %+v", res.Content)
	}
	if storage.sawOpts.LargestAppsN != 3 {
		t.Fatalf("expected LargestAppsN=3, got %d", storage.sawOpts.LargestAppsN)
	}
	if len(storage.sawOpts.AppPaths) != 1 || storage.sawOpts.AppPaths[0] != "/Applications/Slack.app" {
		t.Fatalf("unexpected AppPaths: %v", storage.sawOpts.AppPaths)
	}
}

func TestStorageDefaultsLimit(t *testing.T) {
	storage := &fakeStorageCollector{}
	s := newHostServer(t, Collectors{Storage: storage})
	if res := s.toolStorage(json.RawMessage(`{}`)); res.IsError {
		t.Fatalf("storage errored: %+v", res.Content)
	}
	if storage.sawOpts.LargestAppsN != 10 {
		t.Fatalf("expected default LargestAppsN=10, got %d", storage.sawOpts.LargestAppsN)
	}
}

func TestSystemTopPassesLimit(t *testing.T) {
	sys := &fakeSystemCollector{}
	s := newHostServer(t, Collectors{System: sys})
	if res := s.toolSystem(json.RawMessage(`{"operation":"top","limit":7}`)); res.IsError {
		t.Fatalf("system top errored: %+v", res.Content)
	}
	if sys.sawLimit != 7 {
		t.Fatalf("expected top limit 7, got %d", sys.sawLimit)
	}
}

func TestLogsForwardsQueryFields(t *testing.T) {
	logs := &fakeLogCollector{}
	s := newHostServer(t, Collectors{Logs: logs})
	res := s.toolLogs(json.RawMessage(`{"process":"backupd","last_seconds":120,"max_rows":50}`))
	if res.IsError {
		t.Fatalf("logs errored: %+v", res.Content)
	}
	if logs.sawQ.Process != "backupd" || logs.sawQ.MaxRows != 50 || logs.sawQ.Last != 120*time.Second {
		t.Fatalf("unexpected forwarded query: %+v", logs.sawQ)
	}
}

func TestUpdatesHistoryAndLogOps(t *testing.T) {
	s := newHostServer(t, Collectors{Updates: fakeUpdatesCollector{}})
	if res := s.toolUpdates(json.RawMessage(`{"operation":"history"}`)); res.IsError {
		t.Fatalf("updates history errored: %+v", res.Content)
	}
	if res := s.toolUpdates(json.RawMessage(`{"operation":"log","grep":"macOS"}`)); res.IsError {
		t.Fatalf("updates log errored: %+v", res.Content)
	}
}

func TestServicesAndTimeMachineHappyPath(t *testing.T) {
	s := newHostServer(t, Collectors{
		Services:    fakeServicesCollector{},
		TimeMachine: fakeTimeMachineCollector{},
	})
	if res := s.toolServices(json.RawMessage(`{}`)); res.IsError {
		t.Fatalf("services errored: %+v", res.Content)
	}
	if res := s.toolTimeMachine(json.RawMessage(`{}`)); res.IsError {
		t.Fatalf("timemachine errored: %+v", res.Content)
	}
}

func TestPlaybookListAndGet(t *testing.T) {
	cat := fakePlaybookCatalog{list: []playbook.Playbook{
		{ID: "fseventsd-leak", Title: "fseventsd leak"},
		{ID: "jvm-memory", Title: "JVM memory"},
	}}
	s := newHostServer(t, Collectors{Playbooks: cat})

	res := s.toolPlaybook(json.RawMessage(`{"operation":"list"}`))
	if res.IsError || !strings.Contains(res.Content[0].Text, "found 2 diagnostic playbook") {
		t.Fatalf("unexpected playbook list: %+v", res)
	}

	res = s.toolPlaybook(json.RawMessage(`{"operation":"get","id":"jvm-memory"}`))
	if res.IsError || !strings.Contains(res.Content[0].Text, "jvm-memory") {
		t.Fatalf("unexpected playbook get: %+v", res)
	}
}

// --- error paths -------------------------------------------------------------

func TestPlaybookGetRequiresID(t *testing.T) {
	s := newHostServer(t, Collectors{Playbooks: fakePlaybookCatalog{}})
	if res := s.toolPlaybook(json.RawMessage(`{"operation":"get"}`)); !res.IsError ||
		!strings.Contains(res.Content[0].Text, "requires id") {
		t.Fatalf("expected playbook get id error, got %+v", res)
	}
}

func TestPlaybookGetUnknownID(t *testing.T) {
	s := newHostServer(t, Collectors{Playbooks: fakePlaybookCatalog{}})
	if res := s.toolPlaybook(json.RawMessage(`{"operation":"get","id":"nope"}`)); !res.IsError ||
		!strings.Contains(res.Content[0].Text, "unknown playbook id") {
		t.Fatalf("expected unknown playbook error, got %+v", res)
	}
}

func TestCoreRequiresPath(t *testing.T) {
	s := newHostServer(t, Collectors{})
	if res := s.toolCore(json.RawMessage(`{}`)); !res.IsError ||
		!strings.Contains(res.Content[0].Text, "requires path") {
		t.Fatalf("expected core path error, got %+v", res)
	}
}

func TestUnknownOperationsAreRejected(t *testing.T) {
	s := newHostServer(t, Collectors{
		Power:   &fakePowerCollector{},
		System:  &fakeSystemCollector{},
		Updates: fakeUpdatesCollector{},
	})
	cases := []struct {
		name string
		call func() ToolResult
	}{
		{"power", func() ToolResult { return s.toolPower(json.RawMessage(`{"operation":"bogus"}`)) }},
		{"system", func() ToolResult { return s.toolSystem(json.RawMessage(`{"operation":"bogus"}`)) }},
		{"updates", func() ToolResult { return s.toolUpdates(json.RawMessage(`{"operation":"bogus"}`)) }},
		{"playbook", func() ToolResult { return s.toolPlaybook(json.RawMessage(`{"operation":"bogus"}`)) }},
	}
	for _, tc := range cases {
		res := tc.call()
		if !res.IsError || !strings.Contains(res.Content[0].Text, "unknown "+tc.name+" operation") {
			t.Fatalf("%s: expected unknown-operation error, got %+v", tc.name, res)
		}
	}
}

func TestNewToolSchemasParse(t *testing.T) {
	for _, def := range toolDefinitions() {
		if _, err := json.Marshal(def.InputSchema); err != nil {
			t.Fatalf("tool %q schema failed to marshal: %v", def.Name, err)
		}
	}
}
