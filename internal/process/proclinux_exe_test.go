package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/hostos"
)

// addProc writes a minimal /proc/<pid> entry whose exe link points at target
// (no link when target is empty).
func addProc(t *testing.T, root, pid, comm, cmdline, target string, uid string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "stat"), pid+" ("+comm+") S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0 0 0 0 0 0 0\n")
	mustWrite(t, filepath.Join(dir, "status"), "Uid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\n")
	mustWrite(t, filepath.Join(dir, "cmdline"), strings.ReplaceAll(cmdline, " ", "\x00")+"\x00")
	if target != "" {
		if err := os.Symlink(target, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLinuxExecutablePathFeedsClassify runs the Linux collector and the
// classifier together against a fixture procfs, so neither can paper over
// a missing ExecutablePath for the other.
func TestLinuxExecutablePathFeedsClassify(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "stat"), "btime 1600000000\n")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	tool := filepath.Join(bin, "tool")
	if err := os.Symlink(self, tool); err != nil {
		t.Fatal(err)
	}
	// The kernel's view of an unlinked executable: the link text gains
	// " (deleted)" while the link itself still opens the running image.
	gone := filepath.Join(bin, "gone")
	if err := os.Symlink(self, gone+" (deleted)"); err != nil {
		t.Fatal(err)
	}
	addProc(t, root, "100", "tool", tool+" serve", tool, "1000")
	addProc(t, root, "101", "gone", gone, gone+" (deleted)", "1000")
	// exec -a /usr/sbin/sshd ./evil as root, with /proc/<pid>/exe hidden.
	addProc(t, root, "102", "sshd", "/usr/sbin/sshd -D", "", "0")

	procs := CollectAll(context.Background(), CollectOptions{OS: hostos.Linux, ProcFS: root})
	byPID := map[int]*Info{}
	for i := range procs {
		byPID[procs[i].PID] = &procs[i]
	}
	if p := byPID[100]; p == nil || p.ExecutablePath != tool || p.ExecutableDeleted {
		t.Fatalf("pid 100 = %+v, want ExecutablePath %s", p, tool)
	}
	if p := byPID[101]; p == nil || p.ExecutablePath != gone || !p.ExecutableDeleted {
		t.Fatalf("pid 101 = %+v, want deleted %s", p, gone)
	}
	if p := byPID[102]; p == nil || p.ExecutablePath != "" {
		t.Fatalf("pid 102 = %+v, want no executable path", p)
	}

	c := classify.New(classify.Options{OS: hostos.Linux, ProcFS: root})
	if err := Classify(context.Background(), procs, c); err != nil {
		t.Fatal(err)
	}
	if p := byPID[100]; p.Runtime == classify.RuntimeUnknown {
		t.Fatalf("pid 100 runtime unknown with a readable exe: %v", p.Evidence)
	}
	if p := byPID[101]; p.Runtime == classify.RuntimeUnknown || !strings.Contains(strings.Join(p.Evidence, " "), "was deleted") {
		t.Fatalf("deleted exe not read through /proc: runtime=%s %v", p.Runtime, p.Evidence)
	}
	if p := byPID[102]; p.Role == classify.RoleSystem || p.Runtime != classify.RuntimeUnknown {
		t.Fatalf("argv[0]-only root process = role %s runtime %s, want not system and unknown (%v)", p.Role, p.Runtime, p.Evidence)
	}
}
