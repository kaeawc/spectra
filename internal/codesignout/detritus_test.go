package codesignout

import "testing"

func TestOnlyDetritus(t *testing.T) {
	d := DetritusComplaint
	for out, want := range map[string]bool{
		"/a: " + d + "\n":                                                 true,
		"/a: " + d + "\nIn subcomponent: /a/b\n":                          true,
		"/a: " + d + "\n/a: a sealed resource is missing\n":               false,
		"/a: invalid signature (code or signature have been modified)\n":  false,
		"/a: " + d + "\nIn subcomponent: /a/b\n/a/b: invalid signature\n": false,
		"In subcomponent: /a/b\n":                                         false,
		"":                                                                false,
	} {
		if got := OnlyDetritus(out); got != want {
			t.Errorf("OnlyDetritus(%q) = %v, want %v", out, got, want)
		}
	}
}
