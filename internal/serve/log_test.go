package serve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenLogFileCreatesPrivateJSONLFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Logs", "Spectra", "daemon.jsonl")
	f, err := OpenLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log file mode = %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}\n" {
		t.Fatalf("log contents = %q", string(data))
	}
}

func TestOpenLogFileTightensExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := OpenLogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log file mode = %o, want 0600", info.Mode().Perm())
	}
}
