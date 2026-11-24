package snapshotquery

import (
	"testing"
	"time"
)

func TestDiffOptionsFromArgs(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		since   time.Duration
		want    DiffOptions
		wantErr bool
	}{
		{args: []string{"a", "b"}, want: DiffOptions{A: "a", B: "b"}},
		{args: []string{"baseline"}, want: DiffOptions{Baseline: true}},
		{args: []string{"baseline", "rel"}, want: DiffOptions{Baseline: true, A: "rel"}},
		{args: []string{"baseline", "rel", "snap"}, want: DiffOptions{Baseline: true, A: "rel", B: "snap"}},
		{since: time.Hour, want: DiffOptions{Since: time.Hour}},
		{args: []string{"a"}, wantErr: true},
		{args: nil, wantErr: true},
		{args: []string{"a", "b", "c"}, wantErr: true},
		{args: []string{"baseline", "a", "b", "c"}, wantErr: true},
		{args: []string{"a", "b"}, since: time.Hour, wantErr: true},
	} {
		got, err := DiffOptionsFromArgs(tc.args, tc.since)
		if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
			t.Errorf("%v since=%s: got %+v, %v", tc.args, tc.since, got, err)
		}
		if err == nil {
			if verr := got.Validate(); verr != nil {
				t.Errorf("%v: parsed options fail validation: %v", tc.args, verr)
			}
		}
	}
}

func TestValidKind(t *testing.T) {
	for kind, want := range map[string]bool{"": true, "live": true, "baseline": true, "other": false} {
		if ValidKind(kind) != want {
			t.Errorf("ValidKind(%q) = %v", kind, !want)
		}
	}
}
