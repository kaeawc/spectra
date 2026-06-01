package main

import (
	"testing"
	"time"
)

func TestParseMemoryWatch(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"1", time.Second},
		{"5s", 5 * time.Second},
		{"1m30s", 90 * time.Second},
	}
	for _, tc := range cases {
		got, err := parseMemoryWatch(tc.in)
		if err != nil {
			t.Fatalf("parseMemoryWatch(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseMemoryWatch(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestParseMemoryWatchRejectsNonPositive(t *testing.T) {
	for _, in := range []string{"0", "-1s"} {
		if _, err := parseMemoryWatch(in); err == nil {
			t.Fatalf("parseMemoryWatch(%q) succeeded, want error", in)
		}
	}
}

func TestMemoryRate(t *testing.T) {
	if got := rate(30, 10, 2); got != 10 {
		t.Fatalf("rate = %.1f, want 10", got)
	}
	if got := rate(10, 30, 2); got != 0 {
		t.Fatalf("counter reset rate = %.1f, want 0", got)
	}
}
