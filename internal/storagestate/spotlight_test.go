package storagestate

import (
	"os"
	"reflect"
	"testing"
)

func TestParseMdutilStatusFixtures(t *testing.T) {
	tests := []struct {
		file string
		want map[string]SpotlightStatus
	}{
		{
			file: "testdata/mdutil_macos14.txt",
			want: map[string]SpotlightStatus{
				"/":                       SpotlightEnabled,
				"/System/Volumes/Data":    SpotlightEnabled,
				"/Volumes/Disabled":       SpotlightDisabled,
				"/Volumes/SearchDisabled": SpotlightDisabled,
			},
		},
		{
			file: "testdata/mdutil_macos15.txt",
			want: map[string]SpotlightStatus{
				"/":                    SpotlightError,
				"/System/Volumes/Data": SpotlightNoIndex,
				"/Volumes/Unknown":     SpotlightUnknown,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]SpotlightStatus{}
			for _, volume := range parseMdutilStatus(data) {
				got[volume.MountPoint] = volume.Status
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("statuses = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseMdutilProgress(t *testing.T) {
	got := parseMdutilProgress([]byte("/:\n\tIndexing 42.5%\n/System/Volumes/Data:\n\tScanning 7%\n/Volumes/Idle:\n\tIdle\n"))
	if got["/"].Phase != "Indexing" || got["/"].Percent != 42.5 {
		t.Fatalf("root progress = %+v", got["/"])
	}
	if got["/System/Volumes/Data"].Phase != "Scanning" || got["/System/Volumes/Data"].Percent != 7 {
		t.Fatalf("data progress = %+v", got["/System/Volumes/Data"])
	}
	if got["/Volumes/Idle"].Phase != "Idle" || got["/Volumes/Idle"].Percent != 0 {
		t.Fatalf("idle progress = %+v", got["/Volumes/Idle"])
	}
}

func TestCollectSpotlightFiltersWritableAPFS(t *testing.T) {
	stub := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "/usr/bin/mdutil" && len(args) == 1 && args[0] == "-sa":
			return []byte("/:\n\tIndexing enabled.\n/System/Volumes/Data:\n\tIndexing enabled.\n/System/Volumes/Data:\n\tIndexing enabled.\n/System/Volumes/Preboot:\n\tIndexing disabled.\n"), nil
		case name == "/usr/bin/mdutil" && len(args) == 1 && args[0] == "-p":
			return []byte("/System/Volumes/Data:\n\tIndexing 12%\n"), nil
		case name == "/usr/bin/mdutil" && len(args) == 2 && args[0] == "-s":
			return []byte(args[1] + ":\n\tNo index.\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}
	mounts := []Mount{
		{MountPoint: "/", FSType: "apfs", ReadOnly: true, APFSRole: "system"},
		{MountPoint: "/System/Volumes/Data", FSType: "apfs", APFSRole: "data"},
		{MountPoint: "/System/Volumes/Preboot", FSType: "apfs", APFSRole: "preboot"},
		{MountPoint: "/System/Volumes/Update", FSType: "apfs", APFSRole: "update"},
		{MountPoint: "/Library/Developer/CoreSimulator/Volumes/iOS", FSType: "apfs", ReadOnly: true, APFSRole: "simulator"},
	}
	got := collectSpotlight(stub, mounts)
	if len(got) != 3 {
		t.Fatalf("spotlight volumes = %d, want 3: %+v", len(got), got)
	}
	if got[0].MountPoint != "/System/Volumes/Data" || got[0].Progress == nil {
		t.Fatalf("first volume = %+v, want data with progress", got[0])
	}
	if got[2].MountPoint != "/System/Volumes/Update" || got[2].Status != SpotlightNoIndex {
		t.Fatalf("missing per-mount fallback: %+v", got)
	}
}
