package process

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestCwdLinux(t *testing.T) {
	var asked string
	c := CwdReader{OS: hostos.Linux, Readlink: func(p string) (string, error) {
		asked = p
		return "/srv/app", nil
	}}
	got, err := c.Cwd(context.Background(), 42)
	if err != nil || got != "/srv/app" || asked != "/proc/42/cwd" {
		t.Fatalf("Cwd = %q %v (readlink %q)", got, err, asked)
	}
	c.Readlink = func(string) (string, error) { return "", errors.New("permission denied") }
	if _, err := c.Cwd(context.Background(), 42); err == nil {
		t.Fatal("readlink error swallowed")
	}
}

func TestCwdDarwin(t *testing.T) {
	var args []string
	c := CwdReader{OS: hostos.Darwin, Runner: CommandRunnerFunc(func(_ context.Context, name string, a ...string) ([]byte, error) {
		args = append([]string{name}, a...)
		return []byte("p42\nfcwd\nn/Users/jane/My App\n"), nil
	})}
	got, err := c.Cwd(context.Background(), 42)
	if err != nil || got != "/Users/jane/My App" {
		t.Fatalf("Cwd = %q %v", got, err)
	}
	if want := []string{"lsof", "-a", "-p", "42", "-d", "cwd", "-Fn"}; !reflect.DeepEqual(args, want) {
		t.Errorf("ran %q, want %q", args, want)
	}
	c.Runner = CommandRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	})
	if _, err := c.Cwd(context.Background(), 42); err == nil {
		t.Fatal("lsof failure swallowed")
	}
}

func TestCwdUnsupported(t *testing.T) {
	if _, err := (CwdReader{OS: hostos.Kind(99)}).Cwd(context.Background(), 1); err == nil {
		t.Fatal("unknown OS accepted")
	}
}
