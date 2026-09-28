package hostwatch

import (
	"errors"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte("load:\n  warn_multiple: 2\n"), nil }
	c, err := LoadConfig("x", read)
	if err != nil || c.Load.WarnMultiple != 2 || c.Load.CriticalMultiple != 3 {
		t.Fatalf("config: %+v %v", c, err)
	}
	c, err = LoadConfig("x", func(string) ([]byte, error) { return []byte("unexpected: true"), nil })
	if err == nil || c.Load.WarnMultiple != 1.5 {
		t.Fatalf("unknown key: %+v %v", c, err)
	}
	_, err = LoadConfig("x", func(string) ([]byte, error) { return nil, errors.New("bad") })
	if err == nil {
		t.Fatal("read error lost")
	}
}
