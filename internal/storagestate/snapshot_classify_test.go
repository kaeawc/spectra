package storagestate

import (
	"os"
	"testing"
)

func TestClassifySnapshotName(t *testing.T) {
	tests := []struct {
		name     string
		wantKind SnapshotKind
		wantDate bool
	}{
		{
			name:     "com.apple.os.update-9E3B7EFBB732B00D98BF266D7444FDC8348CC3B2CCEF551FF262CA73E4E23BAE",
			wantKind: SnapshotOSUpdate,
		},
		{
			name:     "com.apple.os.update-MSUPrepareUpdate",
			wantKind: SnapshotMSUPrepare,
		},
		{
			name:     "com.apple.TimeMachine.2026-05-24-135501.local",
			wantKind: SnapshotTMLocal,
			wantDate: true,
		},
		{
			name:     "com.apple.TimeMachine.2026-05-24-135501",
			wantKind: SnapshotTMRemote,
			wantDate: true,
		},
		{
			name:     "manual.snapshot",
			wantKind: SnapshotUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.wantKind.String(), func(t *testing.T) {
			gotKind, gotDate := ClassifySnapshotName(tt.name)
			if gotKind != tt.wantKind {
				t.Fatalf("kind = %s, want %s", gotKind, tt.wantKind)
			}
			if gotDate.IsZero() == tt.wantDate {
				t.Fatalf("created date zero = %v, want date = %v", gotDate.IsZero(), tt.wantDate)
			}
		})
	}
}

func TestParseAPFSSnapshotsFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/apfs-snapshots.plist")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAPFSSnapshots(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(got))
	}
	if got[0].Kind != SnapshotOSUpdate || got[0].UUID == "" || got[0].XID == 0 {
		t.Fatalf("first snapshot parsed incorrectly: %+v", got[0])
	}
	if got[1].Kind != SnapshotMSUPrepare || !got[1].PinsCapacity {
		t.Fatalf("MSUPrepare snapshot parsed incorrectly: %+v", got[1])
	}
}

func TestParseAPFSSnapshotsEmpty(t *testing.T) {
	got, err := ParseAPFSSnapshots([]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>Snapshots</key><array/></dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("snapshots = %d, want 0", len(got))
	}
}
