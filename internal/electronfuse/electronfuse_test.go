package electronfuse

import (
	"errors"
	"testing"
)

// buildWire embeds a fuse status string in a synthetic binary: junk, then the
// sentinel, version, count, the ASCII status bytes, then trailing junk.
func buildWire(version byte, statuses string) []byte {
	w := []byte("some-preceding-bytes-")
	w = append(w, sentinel...)
	w = append(w, version, byte(len(statuses)))
	w = append(w, []byte(statuses)...)
	w = append(w, []byte("-trailing")...)
	return w
}

func TestParseHardened(t *testing.T) {
	// 1Password's real vector: RunAsNode off, inspect off, integrity on.
	cfg, err := Parse(buildWire(1, "000011011"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
	if len(cfg.Fuses) != 9 {
		t.Fatalf("fuses = %d, want 9", len(cfg.Fuses))
	}
	if s, _ := cfg.Get("RunAsNode"); s != StatusDisabled {
		t.Errorf("RunAsNode = %q, want disabled", s)
	}
	if s, _ := cfg.Get("EnableEmbeddedAsarIntegrityValidation"); s != StatusEnabled {
		t.Errorf("integrity = %q, want enabled", s)
	}
	if f := cfg.Audit(); len(f) != 0 {
		t.Errorf("hardened config should have no findings, got %+v", f)
	}
}

func TestParseInsecure(t *testing.T) {
	// RunAsNode on, node-options on, integrity off, only-load-from-asar off.
	cfg, err := Parse(buildWire(1, "101000000"))
	if err != nil {
		t.Fatal(err)
	}
	findings := cfg.Audit()
	bySev := map[string]int{}
	names := map[string]bool{}
	for _, f := range findings {
		bySev[f.Severity]++
		names[f.Fuse] = true
	}
	if bySev["critical"] != 1 || !names["RunAsNode"] {
		t.Errorf("expected a critical RunAsNode finding, got %+v", findings)
	}
	if !names["EnableNodeOptionsEnvironmentVariable"] {
		t.Errorf("expected NODE_OPTIONS finding, got %+v", findings)
	}
	if !names["EnableEmbeddedAsarIntegrityValidation"] {
		t.Errorf("expected integrity-off finding, got %+v", findings)
	}
}

func TestParseNoSentinel(t *testing.T) {
	if _, err := Parse([]byte("nothing to see here")); !errors.Is(err, ErrNoSentinel) {
		t.Fatalf("err = %v, want ErrNoSentinel", err)
	}
}

func TestParseTruncated(t *testing.T) {
	w := append([]byte("x"), sentinel...) // sentinel but no version/count
	if _, err := Parse(w); err == nil {
		t.Fatal("expected error for truncated wire")
	}
}

func TestParseCountExceedsData(t *testing.T) {
	w := append([]byte{}, sentinel...)
	w = append(w, 1, 20)           // count 20
	w = append(w, []byte("00")...) // but only 2 status bytes
	if _, err := Parse(w); err == nil {
		t.Fatal("expected error when count exceeds data")
	}
}

func TestUnknownFuseIndexNamed(t *testing.T) {
	cfg, err := Parse(buildWire(1, "0000000000")) // 10 fuses; index 8,9 beyond v1 names
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Fuses[8].Name != "Fuse8" || cfg.Fuses[9].Name != "Fuse9" {
		t.Errorf("unknown fuse names = %q,%q", cfg.Fuses[8].Name, cfg.Fuses[9].Name)
	}
}

func TestStatusFromByte(t *testing.T) {
	cases := map[byte]Status{'0': StatusDisabled, '1': StatusEnabled, '2': StatusRemoved, 'x': StatusRemoved}
	for b, want := range cases {
		if got := statusFromByte(b); got != want {
			t.Errorf("statusFromByte(%q) = %q, want %q", b, got, want)
		}
	}
}
