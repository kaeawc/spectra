package hostwatch

import (
	"os"
	"testing"
)

func BenchmarkDarwinArgv(b *testing.B) {
	be := platformSpawnBackend{}
	pid := os.Getpid()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := be.Argv(pid); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDarwinArgvSelfAndReuse(t *testing.T) {
	be := platformSpawnBackend{}
	a, err := be.Argv(os.Getpid())
	if err != nil || a == "" {
		t.Fatalf("self argv %q err=%v", a, err)
	}
	if _, err := be.Argv(os.Getppid()); err != nil {
		t.Logf("parent unreadable: %v", err)
	}
	if a2, _ := be.Argv(os.Getpid()); a2 != a {
		t.Fatalf("first result changed after reuse: %q vs %q", a, a2)
	}
}
