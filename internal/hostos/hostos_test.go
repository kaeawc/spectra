package hostos

import (
	"errors"
	"runtime"
	"testing"
)

func TestCurrentMatchesGOOS(t *testing.T) {
	var want Kind
	switch runtime.GOOS {
	case "darwin":
		want = Darwin
	case "linux":
		want = Linux
	default:
		want = Unknown
	}
	if got := Current(); got != want {
		t.Fatalf("Current() = %v, want %v for GOOS %q", got, want, runtime.GOOS)
	}
}

func TestString(t *testing.T) {
	cases := map[Kind]string{
		Darwin:    "darwin",
		Linux:     "linux",
		Unknown:   "unknown",
		Kind(999): "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(k), got, want)
		}
	}
}

func TestSetForTestAndRestore(t *testing.T) {
	restore := SetForTest(Linux)
	if got := Current(); got != Linux {
		t.Fatalf("after SetForTest(Linux), Current() = %v, want Linux", got)
	}
	restore()
	// After restore, Current reflects the real host again.
	if got := Current(); got != fromGOOS() {
		t.Fatalf("after restore, Current() = %v, want %v", got, fromGOOS())
	}
}

func TestResolve(t *testing.T) {
	defer SetForTest(Darwin)()
	if got := Resolve(Unknown); got != Darwin {
		t.Errorf("Resolve(Unknown) = %v, want host Darwin", got)
	}
	if got := Resolve(Linux); got != Linux {
		t.Errorf("Resolve(Linux) = %v, want Linux (explicit wins over host)", got)
	}
}

func TestUnsupportedWrapsSentinel(t *testing.T) {
	defer SetForTest(Linux)()
	err := Unsupported("codesign inspection")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Unsupported error does not wrap ErrUnsupported: %v", err)
	}
	if got := err.Error(); got == "" ||
		!contains(got, "codesign inspection") || !contains(got, "linux") {
		t.Fatalf("Unsupported error = %q, want it to name the feature and OS", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
