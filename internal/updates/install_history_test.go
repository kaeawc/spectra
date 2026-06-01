package updates

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestParseInstallHistoryFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/install_history.xml")
	if err != nil {
		t.Fatal(err)
	}
	collectedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	history, err := ParseInstallHistory(data, collectedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !history.CollectedAt.Equal(collectedAt) {
		t.Fatalf("CollectedAt = %v, want %v", history.CollectedAt, collectedAt)
	}
	if len(history.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2", len(history.Entries))
	}
	first := history.Entries[0]
	if first.Name != "XProtectPlistConfigData" || first.Version != "5278" || first.Source != "Apple" {
		t.Fatalf("first entry = %#v", first)
	}
	if len(first.PackageIDs) != 1 || first.PackageIDs[0] != "com.apple.pkg.XProtectPlistConfigData_10_15.16U5278" {
		t.Fatalf("PackageIDs = %#v", first.PackageIDs)
	}
	second := history.Entries[1]
	if second.Source != "3rd Party" {
		t.Fatalf("second source = %q", second.Source)
	}
}

func TestFilterHistory(t *testing.T) {
	history := InstallHistory{Entries: []InstallEntry{
		{Name: "Apple Config", Source: "Apple", InstallDate: time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)},
		{Name: "ExampleTool", Source: "3rd Party", Version: "1.2.3", InstallDate: time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)},
	}}
	filtered, err := FilterHistory(history, HistoryQuery{
		Since:  time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC),
		Source: "third-party",
		Grep:   `Example.*1\.2`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Entries) != 1 || filtered.Entries[0].Name != "ExampleTool" {
		t.Fatalf("filtered entries = %#v", filtered.Entries)
	}
	if _, err := FilterHistory(history, HistoryQuery{Source: "bad"}); err == nil {
		t.Fatal("expected bad source error")
	}
	if _, err := FilterHistory(history, HistoryQuery{Grep: "["}); err == nil {
		t.Fatal("expected bad grep error")
	}
}

func TestCollectHistoryUsesRunnerOnce(t *testing.T) {
	data, err := os.ReadFile("testdata/install_history.xml")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	history, err := collectHistoryWith(context.Background(), func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "system_profiler" || len(args) != 2 || args[0] != "-xml" || args[1] != "SPInstallHistoryDataType" {
			t.Fatalf("unexpected command %s %#v", name, args)
		}
		return data, nil
	}, func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if len(history.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(history.Entries))
	}
}
