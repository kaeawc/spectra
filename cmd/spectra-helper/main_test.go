package main

import "testing"

func TestHelperVersionArg(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}} {
		if !helperVersionArg(args) {
			t.Fatalf("%v should be a version arg", args)
		}
	}
	for _, args := range [][]string{nil, {"--help"}, {"version", "extra"}} {
		if helperVersionArg(args) {
			t.Fatalf("%v should not be a version arg", args)
		}
	}
}
