package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/contracttest"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/syslimits"
)

// contractSysUID is the uid the system cases count `ps -u` for; the process
// tables give uid 501 to the user's processes.
const contractSysUID = 501

var contractSysKeys = []string{
	"kern.tty.ptmx_max", "kern.num_files", "kern.maxfiles",
	"kern.maxfilesperproc", "kern.maxproc", "kern.maxprocperuid",
}

// contractSys is what a host answers `spectra system limits` with.
type contractSys struct {
	sysctl map[string]string // missing key: sysctl fails
	lsof   string            // `lsof -n -P -d ^txt` output
	lsofAs string            // "", "exit1" (output and exit status 1) or "missing" (no lsof)
	psAll  int               // processes `ps -A` lists
	psUID  int               // processes `ps -u 501` lists
}

func contractSysPTYLsof(n int) string {
	var b strings.Builder
	b.WriteString("COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n")
	b.WriteString("launchd     1 root  cwd    DIR   1,16      640    2 /\n")
	b.WriteString("zsh       700 user    0u   CHR   16,3      0t0 1011 /dev/ttys003\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "App       777 alice %du   CHR    3,2      0t0  339 /dev/ttys%03d\n", i, i)
	}
	b.WriteString("App       777 alice  90u   CHR    3,2      0t0  340 /dev/ptmx\n")
	b.WriteString("App       777 alice  91u   CHR    3,2      0t0  341 /dev/null\n")
	b.WriteString("App       777 alice   9u  PIPE 0x1111 16384      ->0x2222\n")
	return b.String()
}

func contractSysLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%5d\n", i)
	}
	b.WriteString("\n")
	return b.String()
}

// install registers the host's answers on fake.
func (s contractSys) install(fake *proc.Fake) {
	for _, key := range contractSysKeys {
		resp := proc.Response{Result: proc.Result{ExitCode: 1, Stderr: []byte("sysctl: unknown oid '" + key + "'\n")}, Err: errors.New("exit status 1")}
		if v, ok := s.sysctl[key]; ok {
			resp = proc.Response{Result: proc.Result{Stdout: []byte(v + "\n")}}
		}
		fake.OnExact("sysctl", []string{"-n", key}, resp)
	}
	lsof := []string{"-n", "-P", "-d", "^txt"}
	switch s.lsofAs {
	case "missing":
		fake.OnExact("lsof", lsof, proc.Response{Result: proc.Result{ExitCode: -1}, Err: errors.New("exec: \"lsof\": executable file not found in $PATH")})
	case "exit1":
		fake.OnExact("lsof", lsof, proc.Response{Result: proc.Result{Stdout: []byte(s.lsof), ExitCode: 1}})
	default:
		fake.OnExact("lsof", lsof, proc.Response{Result: proc.Result{Stdout: []byte(s.lsof)}})
	}
	fake.OnExact("ps", []string{"-A", "-o", "pid="}, proc.Response{Result: proc.Result{Stdout: []byte(contractSysLines(s.psAll))}})
	fake.OnExact("ps", []string{"-u", fmt.Sprint(contractSysUID), "-o", "pid="}, proc.Response{Result: proc.Result{Stdout: []byte(contractSysLines(s.psUID))}})
}

func contractSysHealthy() contractSys {
	return contractSys{
		sysctl: map[string]string{
			"kern.tty.ptmx_max": "511", "kern.num_files": "4096", "kern.maxfiles": "245760",
			"kern.maxfilesperproc": "122880", "kern.maxproc": "6000", "kern.maxprocperuid": "4000",
		},
		lsof:  contractSysPTYLsof(12),
		psAll: 480, psUID: 310,
	}
}

func contractSysSaturated() contractSys {
	return contractSys{
		sysctl: map[string]string{
			"kern.tty.ptmx_max": "16", "kern.num_files": "90", "kern.maxfiles": "100",
			"kern.maxfilesperproc": "10240", "kern.maxproc": "17", "kern.maxprocperuid": "12",
		},
		lsof:  contractSysPTYLsof(14),
		psAll: 16, psUID: 12,
	}
}

// contractSysRecord runs `spectra <args>` against env, whose runner also
// answers the host's limits, and writes case system/name.
func contractSysRecord(t *testing.T, name string, env *contractProcEnv, host *contractSys, args []string) {
	t.Helper()
	c := contracttest.New(t, "system", name)
	if env != nil {
		env.input["uid"] = contractSysUID
		if host != nil {
			host.install(env.rec.Inner.(*proc.Fake))
		}
	}
	contractProcRun(t, c, env, args, func() (int, []byte, []byte) {
		orig := systemLimitsOptions
		defer func() { systemLimitsOptions = orig }()
		if env != nil {
			now := contractProcTime(t)
			systemLimitsOptions = func() syslimits.Options {
				return syslimits.Options{
					UID: contractSysUID,
					Now: func() time.Time { return now },
					Run: func(name string, args ...string) ([]byte, error) {
						res, err := env.rec.Run(context.Background(), proc.Cmd{Name: name, Args: args})
						if err == nil && res.ExitCode != 0 {
							err = fmt.Errorf("exit status %d", res.ExitCode)
						}
						return res.Stdout, err
					},
				}
			}
		}
		var code int
		stdout, stderr := captureStdio(t, func() { code = dispatch(args) })
		return code, stdout, stderr
	})
}

// TestContractSystemCases records `spectra system limits` (JSON and text) on
// macOS and on a Linux host, where Go still asks the macOS tools: healthy,
// saturated (exit 1), partial collection failures, --top attribution, and the
// usage and flag errors. Replayed by crates/cli/tests/contract.rs.
func TestContractSystemCases(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "")
	darwin := func() *contractProcEnv {
		return contractProcDarwinEnv(t, contractProcDarwinRows(), contractProcDarwinDetails())
	}
	linux := func(name string) *contractProcEnv {
		return contractProcLinuxEnv(t, contracttest.New(t, "system", name))
	}
	for _, mode := range []string{"json", "text"} {
		flag := []string{}
		if mode == "json" {
			flag = []string{"--json"}
		}
		args := func(extra ...string) []string {
			return append(append([]string{"system", "limits"}, flag...), extra...)
		}
		healthy, saturated := contractSysHealthy(), contractSysSaturated()
		contractSysRecord(t, mode+"-darwin", darwin(), &healthy, args())
		contractSysRecord(t, mode+"-darwin-saturated", darwin(), &saturated, args())
		contractSysRecord(t, mode+"-darwin-top", darwin(), &saturated, args("--top"))
		contractSysRecord(t, mode+"-darwin-top-healthy", darwin(), &healthy, args("--top"))

		partial := contractSysHealthy()
		delete(partial.sysctl, "kern.num_files")
		delete(partial.sysctl, "kern.maxprocperuid")
		partial.lsofAs = "missing"
		contractSysRecord(t, mode+"-darwin-partial", darwin(), &partial, args())

		exit1 := contractSysHealthy()
		exit1.lsofAs = "exit1"
		contractSysRecord(t, mode+"-darwin-lsof-exit1", darwin(), &exit1, args())

		// Go does not branch on the OS: a Linux host asks the same tools, most
		// of which fail there.
		lin := contractSys{sysctl: map[string]string{}, lsofAs: "missing", psAll: 9, psUID: 4}
		contractSysRecord(t, mode+"-linux", linux(mode+"-linux"), &lin, args())
		// One resource saturated on the Linux process table.
		linTop := contractSys{sysctl: map[string]string{"kern.maxproc": "9"}, lsofAs: "missing", psAll: 9, psUID: 4}
		contractSysRecord(t, mode+"-linux-top", linux(mode+"-linux-top"), &linTop, args("--top"))
	}
	// Flag parsing stops at the first positional argument, so --json is ignored.
	healthy := contractSysHealthy()
	contractSysRecord(t, "text-darwin-positional-stops-flags", darwin(), &healthy, []string{"system", "limits", "extra", "--json"})
	for name, a := range map[string][]string{
		"usage-none":         {"system"},
		"usage-unknown":      {"system", "nope"},
		"usage-flag-first":   {"system", "--json"},
		"flag-unknown":       {"system", "limits", "--nope"},
		"flag-help":          {"system", "limits", "-h"},
		"flag-bad-bool":      {"system", "limits", "--json=maybe"},
		"flag-top-bad-value": {"system", "limits", "--top=2"},
	} {
		contractSysRecord(t, name, nil, nil, a)
	}
}
