package bundleid

import "testing"

func TestValid(t *testing.T) {
	good := []string{"com.docker.docker", "app.tuple.app", "Test-name_2"}
	for _, s := range good {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}

	bad := []string{"", "evil; DROP TABLE", "with space", "x'; --", "quote'in"}
	for _, s := range bad {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}
