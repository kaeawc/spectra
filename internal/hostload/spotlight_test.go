package hostload

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/storagestate"
)

const exclusionsPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Exclusions</key><array>
<string>/System/Volumes/Data/Users/tester/excluded</string>
<string>/Volumes/Scratch</string>
</array>
</dict></plist>`

type spotFake struct {
	markers map[string]bool
	plist   string // "" means the privacy list is unreadable
	status  storagestate.SpotlightStatus
	volErr  error
}

func (f spotFake) probe() SpotlightProbe {
	return SpotlightProbe{
		OS:     hostos.Darwin,
		Exists: func(p string) bool { return f.markers[p] },
		ReadFile: func(p string) ([]byte, error) {
			if f.plist == "" || p != "/System/Volumes/Data/.Spotlight-V100/VolumeConfiguration.plist" {
				return nil, fs.ErrPermission
			}
			return []byte(f.plist), nil
		},
		Volume: func(context.Context, string) (storagestate.SpotlightVolume, error) {
			if f.volErr != nil {
				return storagestate.SpotlightVolume{}, f.volErr
			}
			return storagestate.SpotlightVolume{MountPoint: "/System/Volumes/Data", Status: f.status, Detail: "Indexing enabled."}, nil
		},
		Size: func(_ context.Context, p string) (RootSize, error) {
			return RootSize{Files: 120000, Bytes: 3 << 30}, nil
		},
	}
}

func TestSpotlightRootStates(t *testing.T) {
	tests := []struct {
		name     string
		fake     spotFake
		root     string
		excluded *bool
		privacy  string
		marker   string
		hint     string
	}{
		{"unreadable list, no marker", spotFake{status: storagestate.SpotlightEnabled}, "/Users/tester/wt",
			nil, PrivacyUnreadable, "", "not readable without root"},
		{"marker in an ancestor", spotFake{status: storagestate.SpotlightEnabled, markers: map[string]bool{"/Users/tester/.metadata_never_index": true}}, "/Users/tester/wt",
			ptr(true), PrivacyUnreadable, "/Users/tester/.metadata_never_index", "already excluded from Spotlight; shrink it (120000 files, 3.0 GiB)"},
		{"privacy list names it", spotFake{status: storagestate.SpotlightEnabled, plist: exclusionsPlist}, "/Users/tester/excluded/wt",
			ptr(true), PrivacyListed, "", "already excluded"},
		{"privacy list readable, not listed", spotFake{status: storagestate.SpotlightEnabled, plist: exclusionsPlist}, "/Users/tester/wt",
			ptr(false), PrivacyNotListed, "", "is not excluded from Spotlight; exclude it"},
		{"volume indexing disabled", spotFake{status: storagestate.SpotlightDisabled, plist: exclusionsPlist}, "/Users/tester/wt",
			ptr(false), PrivacyNotListed, "", "indexing is disabled on /System/Volumes/Data"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fake.probe().Inspect(context.Background(), []Root{{Path: tc.root}})
			if len(got) != 1 {
				t.Fatalf("got %d entries", len(got))
			}
			rs := got[0]
			if (rs.Excluded == nil) != (tc.excluded == nil) || (rs.Excluded != nil && *rs.Excluded != *tc.excluded) {
				t.Fatalf("excluded = %v, want %v", rs.Excluded, tc.excluded)
			}
			if rs.PrivacyList != tc.privacy || rs.Marker != tc.marker || rs.Volume == nil || rs.Size == nil {
				t.Fatalf("entry = %+v", rs)
			}
			if !contains(rs.Hint, tc.hint) {
				t.Fatalf("hint %q lacks %q", rs.Hint, tc.hint)
			}
		})
	}
}

func TestSpotlightVolumeError(t *testing.T) {
	f := spotFake{volErr: errors.New("df failed")}
	rs := f.probe().Inspect(context.Background(), []Root{{Path: "/Users/tester/wt"}})[0]
	if rs.Volume != nil || rs.VolumeError != "df failed" || rs.Hint == "" {
		t.Fatalf("entry = %+v", rs)
	}
}

func TestSpotlightUnsupportedOnLinux(t *testing.T) {
	p := spotFake{}.probe()
	p.OS = hostos.Linux
	rs := p.Inspect(context.Background(), []Root{{Path: "/home/tester/wt"}})[0]
	if rs.Unsupported == "" || rs.Volume != nil || rs.Excluded != nil || rs.Size != nil {
		t.Fatalf("entry = %+v", rs)
	}
	if !errors.Is(hostos.Unsupported("x"), hostos.ErrUnsupported) || !contains(rs.Unsupported, hostos.ErrUnsupported.Error()) {
		t.Fatalf("unsupported = %q", rs.Unsupported)
	}
}

func TestMatchExclusion(t *testing.T) {
	entries := []string{"/System/Volumes/Data/Users/tester/a", "/Users/tester/b/"}
	for dir, want := range map[string]string{
		"/Users/tester/a/wt":                      entries[0],
		"/System/Volumes/Data/Users/tester/b/sub": entries[1],
		"/Users/tester/ab":                        "",
		"/Users/tester":                           "",
	} {
		if got := MatchExclusion(dir, entries); got != want {
			t.Errorf("MatchExclusion(%q) = %q, want %q", dir, got, want)
		}
	}
}

func ptr(b bool) *bool { return &b }
