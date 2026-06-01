package main

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/store"
)

func TestLatestChurnByAppSortsBySpawns(t *testing.T) {
	at := time.Date(2026, 5, 6, 15, 4, 0, 0, time.UTC)
	rows := []store.AppChurnRow{
		{MinuteAt: at, AppPath: "/Applications/Slow.app", Spawns: 1},
		{MinuteAt: at, AppPath: "/Applications/Busy.app", Spawns: 7},
		{MinuteAt: at.Add(-time.Minute), AppPath: "/Applications/Busy.app", Spawns: 99},
	}
	got := latestChurnByApp(rows)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].AppPath != "/Applications/Busy.app" || got[0].Spawns != 7 {
		t.Fatalf("top row = %+v", got[0])
	}
}
