package syslimits

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCollectHealthyMachine(t *testing.T) {
	got := Collect(Options{
		Run: stubRunner(map[string]string{
			"sysctl -n kern.tty.ptmx_max":    "128\n",
			"sysctl -n kern.num_files":       "100\n",
			"sysctl -n kern.maxfiles":        "1000\n",
			"sysctl -n kern.maxfilesperproc": "256\n",
			"sysctl -n kern.maxproc":         "2048\n",
			"sysctl -n kern.maxprocperuid":   "1024\n",
			"lsof -n -P -d ^txt":             lsofPTYFixture(10),
			"ps -A -o pid=":                  "1\n2\n3\n4\n",
			"ps -u 501 -o pid=":              "2\n3\n",
		}),
		UID: 501,
		Now: fixedNow,
	})

	if got.PTY.Current != 10 || got.PTY.Limit != 128 || got.PTY.Warn || got.PTY.Critical {
		t.Fatalf("PTY = %+v, want healthy 10/128", got.PTY)
	}
	if got.Files.Current != 100 || got.Files.Limit != 1000 || got.FilesPerProc != 256 {
		t.Fatalf("files = %+v files/proc=%d, want 100/1000 and 256", got.Files, got.FilesPerProc)
	}
	if got.Procs.Current != 4 || got.ProcsPerUID.Current != 2 {
		t.Fatalf("procs = %+v procs/uid = %+v", got.Procs, got.ProcsPerUID)
	}
	if !got.CollectedAt.Equal(fixedNow()) {
		t.Fatalf("CollectedAt = %s, want %s", got.CollectedAt, fixedNow())
	}
}

func TestCollectWarnCriticalAndSaturated(t *testing.T) {
	got := Collect(Options{
		Run: stubRunner(map[string]string{
			"sysctl -n kern.tty.ptmx_max":    "100\n",
			"sysctl -n kern.num_files":       "98\n",
			"sysctl -n kern.maxfiles":        "100\n",
			"sysctl -n kern.maxfilesperproc": "256\n",
			"sysctl -n kern.maxproc":         "100\n",
			"sysctl -n kern.maxprocperuid":   "100\n",
			"lsof -n -P -d ^txt":             lsofPTYFixture(85),
			"ps -A -o pid=":                  numberedLines(120),
			"ps -u 501 -o pid=":              numberedLines(50),
		}),
		UID: 501,
		Now: fixedNow,
	})

	if !got.PTY.Warn || got.PTY.Critical {
		t.Fatalf("PTY = %+v, want warn only", got.PTY)
	}
	if !got.Files.Critical {
		t.Fatalf("Files = %+v, want critical", got.Files)
	}
	if got.Procs.Pct != 120 || !got.Procs.Critical {
		t.Fatalf("Procs = %+v, want saturated critical 120%%", got.Procs)
	}
	if got.ProcsPerUID.Warn || got.ProcsPerUID.Critical {
		t.Fatalf("ProcsPerUID = %+v, want healthy", got.ProcsPerUID)
	}
	if !got.AnyCritical() {
		t.Fatal("AnyCritical = false, want true")
	}
}

func TestCollectPartialFailures(t *testing.T) {
	got := Collect(Options{
		Run: stubRunner(map[string]string{
			"sysctl -n kern.tty.ptmx_max":    "100\n",
			"sysctl -n kern.maxfiles":        "100\n",
			"sysctl -n kern.maxfilesperproc": "256\n",
			"sysctl -n kern.maxproc":         "100\n",
			"sysctl -n kern.maxprocperuid":   "100\n",
			"lsof -n -P -d ^txt":             "",
			"ps -A -o pid=":                  "1\n",
			"ps -u 501 -o pid=":              "1\n",
		}),
		UID: 501,
		Now: fixedNow,
	})

	want := []string{"kern.num_files"}
	if !reflect.DeepEqual(got.PartialFailures, want) {
		t.Fatalf("PartialFailures = %v, want %v", got.PartialFailures, want)
	}
}

func stubRunner(responses map[string]string) CmdRunner {
	return func(name string, args ...string) ([]byte, error) {
		key := strings.TrimSpace(name + " " + strings.Join(args, " "))
		out, ok := responses[key]
		if !ok {
			return nil, fmt.Errorf("unexpected command: %s", key)
		}
		return []byte(out), nil
	}
}

func lsofPTYFixture(n int) string {
	var b strings.Builder
	b.WriteString("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "App 777 alice %du CHR 3,2 0t0 339 /dev/ttys%03d\n", i, i)
	}
	return b.String()
}

func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String()
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
}
