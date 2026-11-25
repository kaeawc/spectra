//go:build helper_vm && darwin

// The VM-only integration suite for the helper's launchd mutation methods
// (docs/design/helper-launchd-mutation.md#testing). It changes real system
// state as root: it installs a fixture app in /Applications and plists in
// /Library/LaunchDaemons, and drives the real helper code over a private
// Unix socket against the real launchctl, codesign and sfltool. It runs only
// inside the disposable Tart VM that scripts/helper-vm-test.sh creates, with
// SPECTRA_VM_TEST=1, and refuses to run on anything that is not a VM.
//
// Inputs, all set by scripts/helper-vm/guest.sh:
//
//	SPECTRA_VM_TEST=1             opt in
//	SPECTRA_VM_FIXTURE_APP=<path> the fixture app, signed with the VM-only
//	                              Developer ID test certificate
//	SPECTRA_VM_NONROOT_UID=<uid>  a non-root uid for the ownership refusal
//	SPECTRA_VM_AUDIT_LOG=<path>   optional; the helper audit lines are
//	                              appended there for the results bundle
package helper_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/helper"
	"github.com/kaeawc/spectra/internal/helperclient"
)

const (
	vmLabel        = "dev.spectra.vmfixture.agentd"
	vmSymlinkLabel = "dev.spectra.vmfixture.symlink"
	vmBundleID     = "dev.spectra.vmfixture"
	vmAppPath      = "/Applications/SpectraVMFixture.app"
	vmProgram      = vmAppPath + "/Contents/Library/LaunchServices/" + vmLabel
	vmManagedDir   = "/Library/Managed Preferences"
	vmManagedPlist = vmManagedDir + "/com.apple.servicemanagement.plist"
)

// requireVM skips without the opt-in and fails loudly when the opt-in is
// set somewhere the suite must not run.
func requireVM(t *testing.T) {
	t.Helper()
	if os.Getenv("SPECTRA_VM_TEST") != "1" {
		t.Skip("helper_vm suite: set SPECTRA_VM_TEST=1, and run it only inside the disposable VM (scripts/helper-vm-test.sh)")
	}
	if os.Geteuid() != 0 {
		t.Fatal("helper_vm suite must run as root inside the disposable VM")
	}
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "kern.hv_vmm_present").Output()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("refusing to run: kern.hv_vmm_present is %q (err %v); this suite changes system launch daemons and runs only in a VM", strings.TrimSpace(string(out)), err)
	}
	if src := os.Getenv("SPECTRA_VM_FIXTURE_APP"); src == "" {
		t.Fatal("SPECTRA_VM_FIXTURE_APP is not set; scripts/helper-vm/guest.sh builds and signs the fixture app")
	}
}

// syncWriter collects audit lines; the dispatcher writes from handler
// goroutines.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	out io.Writer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if w.out != nil {
		_, _ = w.out.Write(p)
	}
	return len(p), nil
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// startHelper serves a fresh helper (real commands, real filesystem) on a
// private socket, so the installed helper and /var/run are never touched.
func startHelper(t *testing.T) (*helperclient.Client, *syncWriter) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spx-vm-")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "helper.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	audit := &syncWriter{}
	if p := os.Getenv("SPECTRA_VM_AUDIT_LOG"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		audit.out = f
	}
	d := helper.NewDispatcher()
	d.SetAuditWriter(audit)
	helper.RegisterAll(d, nil)
	go func() { _ = d.ServeListener(ln) }()
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	})
	return helperclient.NewWithPath(sock), audit
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func loaded(label string) bool {
	return exec.Command("/bin/launchctl", "print", "system/"+label).Run() == nil
}

// disabled reads launchd's override; macOS 13+ prints "disabled", older
// releases print "true".
func disabled(t *testing.T, label string) bool {
	t.Helper()
	out := run(t, "/bin/launchctl", "print-disabled", "system")
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=>")
		if ok && strings.Trim(strings.TrimSpace(key), `"`) == label {
			val = strings.TrimSpace(val)
			return val == "disabled" || val == "true"
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func plistFor(label string) string {
	return "/Library/LaunchDaemons/" + label + ".plist"
}

func daemonPlist(label, program string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>Program</key>
	<string>%s</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`, label, program))
}

// writeRootFile writes path as root:wheel 0644, the shape the helper accepts.
func writeRootFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// removeDaemon unloads and forgets a fixture label, best effort, so every
// test starts and ends with no job, no override and no plist.
func removeDaemon(label string) {
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+label).Run()
	_ = exec.Command("/bin/launchctl", "enable", "system/"+label).Run()
	_ = os.Remove(plistFor(label))
}

// installFixture copies the signed fixture app into /Applications as root
// and writes the daemon plist. load bootstraps it as a running daemon.
func installFixture(t *testing.T, load bool) {
	t.Helper()
	removeDaemon(vmLabel)
	_ = os.RemoveAll(vmAppPath)
	t.Cleanup(func() {
		removeDaemon(vmLabel)
		_ = os.RemoveAll(vmAppPath)
	})
	run(t, "/usr/bin/ditto", os.Getenv("SPECTRA_VM_FIXTURE_APP"), vmAppPath)
	run(t, "/usr/sbin/chown", "-R", "root:wheel", vmAppPath)
	run(t, "/bin/chmod", "-R", "go-w", vmAppPath)
	run(t, "/usr/bin/codesign", "--verify", "--strict", vmAppPath)
	writeRootFile(t, plistFor(vmLabel), daemonPlist(vmLabel, vmProgram))
	if load {
		run(t, "/bin/launchctl", "bootstrap", "system", plistFor(vmLabel))
		waitFor(t, vmLabel+" to load", func() bool { return loaded(vmLabel) })
	}
}

func prepare(t *testing.T, c *helperclient.Client, label, action string) helper.LaunchdPrepareResult {
	t.Helper()
	res, err := c.LaunchdSystemPrepare(label, action)
	if err != nil {
		t.Fatalf("prepare %s %s: %v", action, label, err)
	}
	return res
}

func prepareOK(t *testing.T, c *helperclient.Client, label, action string) helper.LaunchdPrepareResult {
	t.Helper()
	res := prepare(t, c, label, action)
	if res.Refused {
		t.Fatalf("prepare %s %s refused: %s: %s", action, label, res.Reason, res.Message)
	}
	if res.Token == "" || res.Target == nil || res.Before == nil {
		t.Fatalf("prepare %s %s: incomplete result %+v", action, label, res)
	}
	return res
}

func change(t *testing.T, c *helperclient.Client, action, token string) helper.LaunchdChangeResult {
	t.Helper()
	res, err := c.LaunchdSystemChange(action, token)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return res
}

func wantPlanned(t *testing.T, got [][]string, want ...[]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("planned = %q, want %q", got, want)
	}
}

func wantRefused(t *testing.T, refused bool, reason, gotReason, msg string) {
	t.Helper()
	if !refused || gotReason != reason {
		t.Fatalf("want refusal %s, got refused=%v reason=%q message=%q", reason, refused, gotReason, msg)
	}
}

func checkTarget(t *testing.T, got *helper.LaunchdTarget) {
	t.Helper()
	if got.PlistPath != plistFor(vmLabel) || got.Program != vmProgram || got.BundlePath != vmAppPath || got.BundleID != vmBundleID {
		t.Fatalf("target = %+v", got)
	}
	if len(got.TeamID) != 10 {
		t.Fatalf("team id %q: want the 10-character team of the VM test certificate", got.TeamID)
	}
	t.Logf("verified fixture: team %s, plist sha256 %s", got.TeamID, got.PlistSHA256)
}

// checkAudit requires that the token never reaches the log and that the
// prepare and execute lines pair by token_hash.
func checkAudit(t *testing.T, audit *syncWriter, token string) {
	t.Helper()
	log := audit.String()
	if strings.Contains(log, token) {
		t.Fatal("the audit log contains a confirmation token")
	}
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"label":"`+vmLabel+`"`) && strings.Contains(line, `"token_hash":"`) {
			n++
		}
	}
	if n < 2 {
		t.Fatalf("want a prepare and an execute line with token_hash for %s, got %d:\n%s", vmLabel, n, log)
	}
}

func TestVMDisableThenEnable(t *testing.T) {
	requireVM(t)
	installFixture(t, true)
	c, audit := startHelper(t)

	prep := prepareOK(t, c, vmLabel, "disable")
	checkTarget(t, prep.Target)
	if !prep.Before.Loaded {
		t.Fatalf("before = %+v, want loaded", prep.Before)
	}
	wantPlanned(t, prep.Planned,
		[]string{"/bin/launchctl", "disable", "system/" + vmLabel},
		[]string{"/bin/launchctl", "bootout", "system/" + vmLabel})

	res := change(t, c, "disable", prep.Token)
	if res.Outcome != helper.OutcomeOK || res.After == nil || res.After.Loaded || !res.After.Disabled {
		t.Fatalf("disable = %+v (after %+v)", res, res.After)
	}
	if loaded(vmLabel) || !disabled(t, vmLabel) {
		t.Fatal("launchctl disagrees with the disable result")
	}
	checkAudit(t, audit, prep.Token)

	prepE := prepareOK(t, c, vmLabel, "enable")
	wantPlanned(t, prepE.Planned,
		[]string{"/bin/launchctl", "enable", "system/" + vmLabel},
		[]string{"/bin/launchctl", "bootstrap", "system", plistFor(vmLabel)})
	resE := change(t, c, "enable", prepE.Token)
	if resE.Outcome != helper.OutcomeOK || resE.After == nil || !resE.After.Loaded || resE.After.Disabled {
		t.Fatalf("enable = %+v (after %+v)", resE, resE.After)
	}
	waitFor(t, vmLabel+" to load after enable", func() bool { return loaded(vmLabel) })
	if disabled(t, vmLabel) {
		t.Fatal("launchctl still reports the override after enable")
	}
}

func TestVMReplayedTokenActsOnce(t *testing.T) {
	requireVM(t)
	installFixture(t, true)
	c, _ := startHelper(t)

	prep := prepareOK(t, c, vmLabel, "disable")
	if res := change(t, c, "disable", prep.Token); res.Outcome != helper.OutcomeOK {
		t.Fatalf("first disable = %+v", res)
	}
	// Put the daemon back by hand so a replay that acted would be visible.
	run(t, "/bin/launchctl", "enable", "system/"+vmLabel)
	run(t, "/bin/launchctl", "bootstrap", "system", plistFor(vmLabel))
	waitFor(t, vmLabel+" to reload", func() bool { return loaded(vmLabel) })

	again := change(t, c, "disable", prep.Token)
	wantRefused(t, again.Refused, helper.ReasonTokenInvalid, again.Reason, again.Message)
	if len(again.Commands) != 0 {
		t.Fatalf("replayed token ran %v", again.Commands)
	}
	if !loaded(vmLabel) || disabled(t, vmLabel) {
		t.Fatal("a replayed token changed the daemon")
	}
}

func TestVMEnableBootstrapsUnloadedDaemon(t *testing.T) {
	requireVM(t)
	installFixture(t, false)
	run(t, "/bin/launchctl", "disable", "system/"+vmLabel)
	c, _ := startHelper(t)

	prep := prepareOK(t, c, vmLabel, "enable")
	if prep.Before.Loaded || !prep.Before.Disabled {
		t.Fatalf("before = %+v, want unloaded and disabled", prep.Before)
	}
	wantPlanned(t, prep.Planned,
		[]string{"/bin/launchctl", "enable", "system/" + vmLabel},
		[]string{"/bin/launchctl", "bootstrap", "system", plistFor(vmLabel)})
	res := change(t, c, "enable", prep.Token)
	if res.Outcome != helper.OutcomeOK || res.After == nil || !res.After.Loaded {
		t.Fatalf("enable = %+v (after %+v)", res, res.After)
	}
	waitFor(t, vmLabel+" to load", func() bool { return loaded(vmLabel) })
}

func TestVMTargetChangedAfterPrepare(t *testing.T) {
	requireVM(t)
	installFixture(t, true)
	c, _ := startHelper(t)

	prep := prepareOK(t, c, vmLabel, "disable")
	edited := bytes.Replace(daemonPlist(vmLabel, vmProgram), []byte("<key>RunAtLoad</key>"), []byte("<key>ThrottleInterval</key>\n\t<integer>30</integer>\n\t<key>RunAtLoad</key>"), 1)
	writeRootFile(t, plistFor(vmLabel), edited)

	res := change(t, c, "disable", prep.Token)
	wantRefused(t, res.Refused, helper.ReasonTargetChanged, res.Reason, res.Message)
	if !loaded(vmLabel) || disabled(t, vmLabel) {
		t.Fatal("a refused change touched the daemon")
	}
}

func TestVMRefusesAppleLabel(t *testing.T) {
	requireVM(t)
	c, _ := startHelper(t)
	res := prepare(t, c, "com.apple.spectra.vmtest", "disable")
	wantRefused(t, res.Refused, helper.ReasonAppleLabel, res.Reason, res.Message)
}

func TestVMRefusesSymlinkedPlist(t *testing.T) {
	requireVM(t)
	installFixture(t, false)
	dir, err := os.MkdirTemp("/private/var/tmp", "spx-vm-")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, vmSymlinkLabel+".plist")
	writeRootFile(t, target, daemonPlist(vmSymlinkLabel, vmProgram))
	link := plistFor(vmSymlinkLabel)
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(link)
		_ = os.RemoveAll(dir)
	})
	c, _ := startHelper(t)
	res := prepare(t, c, vmSymlinkLabel, "disable")
	wantRefused(t, res.Refused, helper.ReasonSymlink, res.Reason, res.Message)
	if loaded(vmSymlinkLabel) || disabled(t, vmSymlinkLabel) {
		t.Fatal("a refused prepare touched launchd")
	}
}

func TestVMRefusesNonRootPlist(t *testing.T) {
	requireVM(t)
	uid, err := strconv.Atoi(os.Getenv("SPECTRA_VM_NONROOT_UID"))
	if err != nil || uid == 0 {
		t.Fatalf("SPECTRA_VM_NONROOT_UID = %q: want a non-root uid", os.Getenv("SPECTRA_VM_NONROOT_UID"))
	}
	installFixture(t, false)
	if err := os.Chown(plistFor(vmLabel), uid, 20); err != nil {
		t.Fatal(err)
	}
	c, _ := startHelper(t)
	res := prepare(t, c, vmLabel, "disable")
	wantRefused(t, res.Refused, helper.ReasonPlistPermissions, res.Reason, res.Message)
	if disabled(t, vmLabel) {
		t.Fatal("a refused prepare added a launchd override")
	}
}

func TestVMRefusesServiceManagementRule(t *testing.T) {
	requireVM(t)
	installFixture(t, true)
	if _, err := os.Lstat(vmManagedDir); os.IsNotExist(err) {
		if err := os.Mkdir(vmManagedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(vmManagedDir) })
	}
	if _, err := os.Lstat(vmManagedPlist); err == nil {
		t.Fatalf("%s already exists; the VM image must not carry a service-management payload", vmManagedPlist)
	}
	t.Cleanup(func() { _ = os.Remove(vmManagedPlist) })
	writeRootFile(t, vmManagedPlist, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Rules</key>
	<array>
		<dict>
			<key>RuleType</key>
			<string>Label</string>
			<key>RuleValue</key>
			<string>`+vmLabel+`</string>
		</dict>
	</array>
</dict>
</plist>
`))
	c, _ := startHelper(t)
	res := prepare(t, c, vmLabel, "disable")
	wantRefused(t, res.Refused, helper.ReasonManaged, res.Reason, res.Message)
	if !loaded(vmLabel) || disabled(t, vmLabel) {
		t.Fatal("a refused prepare touched the daemon")
	}
}
