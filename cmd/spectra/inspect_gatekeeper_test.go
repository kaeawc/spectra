package main

import (
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
)

func TestGatekeeperLine(t *testing.T) {
	cases := []struct {
		name string
		r    detect.Result
		want string
	}{
		{"assessed", detect.Result{GatekeeperStatus: "accepted", GatekeeperSource: "Notarized Developer ID", GatekeeperLikely: "accepted"}, "accepted (Notarized Developer ID)"},
		{"assessed rejected", detect.Result{GatekeeperStatus: "rejected"}, "rejected"},
		{"inferred", detect.Result{GatekeeperLikely: "accepted", GatekeeperLikelySource: "Mac App Store"}, "likely accepted (Mac App Store, from signature; not assessed)"},
		{"inferred, assessment timed out", detect.Result{GatekeeperLikely: "accepted", GatekeeperLikelySource: "Notarized Developer ID", GatekeeperError: "timeout"},
			"likely accepted (Notarized Developer ID, from signature; not assessed); spctl: timeout"},
		{"seal broken", detect.Result{GatekeeperLikelyReason: detect.GatekeeperReasonSealInvalid},
			"likely not accepted: resource seal invalid (from signature; not assessed)"},
		{"seal unchecked", detect.Result{GatekeeperLikelyReason: detect.GatekeeperReasonSealUnchecked + ": timeout"},
			"unknown: seal not checked: timeout (from signature; not assessed)"},
		{"timed out, nothing inferred", detect.Result{GatekeeperError: "timeout"}, "spctl: timeout"},
		{"nothing", detect.Result{}, ""},
	}
	for _, tc := range cases {
		if got := gatekeeperLine(tc.r); got != tc.want {
			t.Errorf("%s: gatekeeperLine = %q, want %q", tc.name, got, tc.want)
		}
	}
}
