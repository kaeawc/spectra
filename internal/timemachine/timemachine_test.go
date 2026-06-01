package timemachine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/plistread"
)

func TestParseDestinationsLocal(t *testing.T) {
	got, err := ParseDestinations(readFixture(t, "one-local.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("destinations len = %d", len(got))
	}
	d := got[0]
	if d.ID != "LOCAL-UUID" || d.Name != "Backup SSD" || d.Kind != "Local" || d.MountPoint != "/Volumes/Backup" {
		t.Fatalf("destination = %+v", d)
	}
	if d.BytesAvailable != 1000 || d.BytesUsed != 2000 || d.QuotaGB != 500 || !d.Encrypted {
		t.Fatalf("destination capacity = %+v", d)
	}
}

func TestParseDestinationsNetwork(t *testing.T) {
	got, err := ParseDestinations(readFixture(t, "one-network.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "smb://nas/TimeMachine" || got[0].Kind != "Network" {
		t.Fatalf("network destination = %+v", got)
	}
}

func TestParseDestinationsEmptyDict(t *testing.T) {
	got, err := ParseDestinations([]byte(`<plist version="1.0"><dict></dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("destinations len = %d, want 0", len(got))
	}
}

func TestParseStatusMidBackup(t *testing.T) {
	got, err := ParseStatus(readFixture(t, "mid-backup-status.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Running || got.Percent != 42.5 || got.BackupPhase != "Copying" || got.DestinationID != "LOCAL-UUID" {
		t.Fatalf("status = %+v", got)
	}
}

func TestParseLocalSnapshots(t *testing.T) {
	got, err := ParseLocalSnapshots(readFixture(t, "local-snapshots.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("snapshots len = %d", len(got))
	}
	if got[0].Name != "com.apple.TimeMachine.2026-05-28-133824.local" || got[0].Date.IsZero() {
		t.Fatalf("snapshot[0] = %+v", got[0])
	}
	if got[1].Volume != "/" {
		t.Fatalf("snapshot[1] = %+v", got[1])
	}
}

func TestCollectNoDestinations(t *testing.T) {
	now := time.Date(2026, 5, 28, 13, 38, 24, 0, time.UTC)
	run := fakeRunner{
		responses: map[string]fakeResponse{
			"/usr/bin/tmutil\x00destinationinfo\x00-X":                 {out: []byte("No destinations configured."), err: errors.New("exit 1")},
			"/usr/bin/tmutil\x00status\x00-X":                          {out: readFixture(t, "idle-status.plist")},
			"/usr/bin/tmutil\x00listlocalsnapshots\x00/\x00-X":         {out: readFixture(t, "empty-array.plist")},
			"/bin/launchctl\x00print\x00system/com.apple.backupd-auto": {out: []byte("service active")},
		},
	}
	got, err := CollectWithOptions(context.Background(), Options{
		Runner: run,
		Now:    func() time.Time { return now },
		ReadPrefs: func() (plistread.TMPrefs, error) {
			return plistread.TMPrefs{AutoBackup: false, PreferencesVersion: 5}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.CollectedAt.Equal(now) || len(got.Destinations) != 0 || !got.SchedulerLoaded || got.AutoBackupEnabled {
		t.Fatalf("state = %+v", got)
	}
}

func TestCollectPermissionDenied(t *testing.T) {
	run := fakeRunner{responses: map[string]fakeResponse{
		"/usr/bin/tmutil\x00destinationinfo\x00-X": {out: []byte("Operation not permitted"), err: errors.New("exit 1")},
	}}
	_, err := CollectWithOptions(context.Background(), Options{Runner: run})
	if !errors.Is(err, ErrNeedsFullDiskAccess) {
		t.Fatalf("err = %v, want ErrNeedsFullDiskAccess", err)
	}
}

func TestLaunchctlNotLoaded(t *testing.T) {
	loaded, err := launchctlLoaded(context.Background(), fakeRunner{responses: map[string]fakeResponse{
		"/bin/launchctl\x00print\x00system/com.apple.backupd-auto": {out: []byte("Could not find service"), err: errors.New("exit 113")},
	}}, "system/com.apple.backupd-auto")
	if err != nil {
		t.Fatal(err)
	}
	if loaded {
		t.Fatal("loaded = true, want false")
	}
}

func TestAnySlice(t *testing.T) {
	if got := anySlice("one"); !reflect.DeepEqual(got, []any{"one"}) {
		t.Fatalf("anySlice scalar = %#v", got)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type fakeResponse struct {
	out []byte
	err error
}

type fakeRunner struct {
	responses map[string]fakeResponse
}

func (f fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += "\x00" + arg
	}
	resp, ok := f.responses[key]
	if !ok {
		return nil, errors.New("unexpected command: " + key)
	}
	return resp.out, resp.err
}
