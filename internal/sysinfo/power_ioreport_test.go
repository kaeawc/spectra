package sysinfo

import (
	"testing"
	"time"
)

func TestSoCPowerWatts(t *testing.T) {
	tests := []struct {
		name string
		p    SoCPower
		want float64
	}{
		{
			name: "joules over second",
			p:    SoCPower{PackageJoules: 8.6, Interval: time.Second},
			want: 8.6,
		},
		{
			name: "joules over 500ms",
			p:    SoCPower{PackageJoules: 4.0, Interval: 500 * time.Millisecond},
			want: 8.0,
		},
		{
			name: "zero interval returns 0",
			p:    SoCPower{PackageJoules: 1.0},
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Watts(); got != tc.want {
				t.Fatalf("Watts() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSampleSoCPowerNegativeInterval(t *testing.T) {
	if _, err := SampleSoCPower(0); err == nil {
		t.Fatal("expected error for zero interval")
	}
	if _, err := SampleSoCPower(-time.Second); err == nil {
		t.Fatal("expected error for negative interval")
	}
}
