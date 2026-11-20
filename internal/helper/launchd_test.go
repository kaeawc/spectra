package helper

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

func TestLaunchdPrepareDisableHappyPath(t *testing.T) {
	w := newWorld(t)
	p := w.prepare(501, testLabel, "disable")
	if p.Refused {
		t.Fatalf("refused: %s %s", p.Reason, p.Message)
	}
	if p.Target.PlistPath != testPlist || p.Target.Program != testProgram || p.Target.BundlePath != testBundle ||
		p.Target.BundleID != "com.example.app" || p.Target.TeamID != testTeam || len(p.Target.PlistSHA256) != 64 {
		t.Fatalf("target = %+v", p.Target)
	}
	if !p.Before.Loaded || !p.Before.Running || p.Before.PID != 412 {
		t.Fatalf("before = %+v", p.Before)
	}
	if got := planString(p.Planned); got != "/bin/launchctl disable system/com.example.agentd;/bin/launchctl bootout system/com.example.agentd" {
		t.Fatalf("planned = %s", got)
	}
	if p.ExpiresAt != "2026-11-20T17:01:00Z" || len(p.Token) != 22 {
		t.Fatalf("token %q expires %q", p.Token, p.ExpiresAt)
	}
	if len(w.host.mutations()) != 0 {
		t.Fatalf("prepare mutated: %v", w.host.mutations())
	}
	r := w.execute(501, "disable", p.Token)
	if r.Refused || r.Outcome != OutcomeOK || r.Mismatch != "" || r.Via != "spectra-helper" {
		t.Fatalf("result = %+v", r)
	}
	if got := strings.Join(w.host.mutations(), ";"); got != "disable system/com.example.agentd;bootout system/com.example.agentd" {
		t.Fatalf("mutations = %s", got)
	}
	if r.After == nil || r.After.Loaded || !r.After.Disabled {
		t.Fatalf("after = %+v", r.After)
	}
	assertAuditPairs(t, w.audit.String(), p.Token)
}

func planString(planned [][]string) string {
	var parts []string
	for _, a := range planned {
		parts = append(parts, strings.Join(a, " "))
	}
	return strings.Join(parts, ";")
}

// assertAuditPairs checks the prepare and execution lines share token_hash
// and nothing in the log holds the token.
func assertAuditPairs(t *testing.T, log, token string) {
	t.Helper()
	if strings.Contains(log, token) {
		t.Fatalf("audit log contains the token:\n%s", log)
	}
	want := tokenHashPrefix(token)
	var prepared, executed bool
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		switch ev["event"] {
		case "launchd.system.prepare":
			prepared = prepared || ev["token_hash"] == want
		case "launchd.system.disable", "launchd.system.enable":
			executed = executed || (ev["token_hash"] == want && ev["commands"] != nil && ev["team_id"] == testTeam)
		}
	}
	if !prepared || !executed {
		t.Fatalf("audit missing paired lines for %s:\n%s", want, log)
	}
}

func TestLaunchdEnableBootstrapsUnloadedDaemon(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) { h.loaded, h.disabled, h.pid = false, true, 0 })
	p := w.prepare(501, testLabel, "enable")
	if p.Refused {
		t.Fatalf("refused: %s", p.Message)
	}
	if got := planString(p.Planned); got != "/bin/launchctl enable system/com.example.agentd;/bin/launchctl bootstrap system "+testPlist {
		t.Fatalf("planned = %s", got)
	}
	r := w.execute(501, "enable", p.Token)
	if r.Outcome != OutcomeOK || r.After == nil || !r.After.Loaded || r.After.Disabled {
		t.Fatalf("result = %+v after %+v", r, r.After)
	}
	if got := strings.Join(w.host.mutations(), ";"); got != "enable system/com.example.agentd;bootstrap system "+testPlist {
		t.Fatalf("mutations = %s", got)
	}
}

func TestLaunchdEnableLoadedDaemonOnlyEnables(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) { h.disabled = true })
	p := w.prepare(0, testLabel, "enable")
	if got := planString(p.Planned); got != "/bin/launchctl enable system/com.example.agentd" {
		t.Fatalf("planned = %s", got)
	}
}

func TestLaunchdDisableUnloadedDaemonPlansNoBootout(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) { h.loaded = false })
	p := w.prepare(501, testLabel, "disable")
	if got := planString(p.Planned); got != "/bin/launchctl disable system/com.example.agentd" {
		t.Fatalf("planned = %s", got)
	}
}

// One case per refusal reason; none may run a launchctl mutation.
func TestLaunchdRefusals(t *testing.T) {
	cases := []struct {
		name   string
		label  string
		uid    uint32
		setup  func(w *world)
		reason string
	}{
		{"non-admin caller", "", 502, nil, ReasonNotAdmin},
		{"admin lookup fails", "", 503, func(w *world) {
			w.svc.isAdmin = func(context.Context, uint32) (bool, error) { return false, errors.New("ds down") }
		}, ReasonNotAdmin},
		{"denylisted label prefix", "", 0, func(w *world) { writeDenylist(w, "<string>com.example.*</string>", "") }, ReasonDenylisted},
		{"denylisted exact label", "", 0, func(w *world) { writeDenylist(w, "<string>COM.EXAMPLE.AGENTD</string>", "") }, ReasonDenylisted},
		{"denylisted team id", "", 0, func(w *world) { writeDenylist(w, "", "<string>"+testTeam+"</string>") }, ReasonDenylisted},
		{"denylist wrong owner", "", 0, func(w *world) { writeDenylist(w, "", ""); w.fs.chown(DenylistPath, 501, 0) }, ReasonDenylistInvalid},
		{"denylist mode 0664", "", 0, func(w *world) { writeDenylist(w, "", ""); w.fs.chmod(DenylistPath, 0o664) }, ReasonDenylistInvalid},
		{"denylist symlink", "", 0, func(w *world) {
			w.fs.dir(DenylistDir, 0, 0, 0o755)
			w.fs.file("/tmp/d.plist", "<plist><dict/></plist>", 0, 0, 0o644)
			w.fs.symlink(DenylistPath, "/tmp/d.plist")
		}, ReasonDenylistInvalid},
		{"denylist malformed", "", 0, func(w *world) {
			w.fs.dir(DenylistDir, 0, 0, 0o755)
			w.fs.file(DenylistPath, "<plist><dict><key>Labels</key><string>x</string></dict></plist>", 0, 0, 0o644)
		}, ReasonDenylistInvalid},
		{"denylist bad entry", "", 0, func(w *world) { writeDenylist(w, "<string>bad label!</string>", "") }, ReasonDenylistInvalid},
		{"denylist dir writable", "", 0, func(w *world) { writeDenylist(w, "", ""); w.fs.chmod(DenylistDir, 0o777) }, ReasonDenylistInvalid},
		{"apple label", "com.apple.fake", 0, nil, ReasonAppleLabel},
		{"bare com.apple label", "com.apple", 0, nil, ReasonAppleLabel},
		{"bare com.apple label, any case", "COM.Apple", 0, nil, ReasonAppleLabel},
		{"default denylist applies without a file", "com.crowdstrike.falcon.Agent", 0, nil, ReasonDenylisted},
		{"BTM record whose Identifier is the label", "", 0, func(w *world) {
			w.host.btm = strings.Replace(strings.Replace(testBTM, "[ legacy ] (0x1)", "[ legacy, managed ] (0x9)", 1),
				"Identifier: 16.com.example.agentd\n                  URL: file:///Library/LaunchDaemons/com.example.agentd.plist\n",
				"Identifier: COM.EXAMPLE.AGENTD\n", 1)
		}, ReasonManaged},
		{"helper's own label", HelperLabel, 0, nil, ReasonDenylisted},
		{"unknown label", "com.example.missing", 0, nil, ReasonUnknownLabel},
		{"label in another file", "com.example.renamed", 0, func(w *world) {
			w.fs.file(launchDaemonsDir+"/other-name.plist", daemonPlist("com.example.renamed", testProgram, ""), 0, 0, 0o644)
		}, ReasonLabelFileMismatch},
		{"plist label differs", "", 0, func(w *world) { w.fs.rewrite(testPlist, daemonPlist("com.example.else", testProgram, "")) }, ReasonLabelFileMismatch},
		{"plist symlink", "", 0, func(w *world) {
			w.fs.file("/tmp/x.plist", daemonPlist(testLabel, testProgram, ""), 0, 0, 0o644)
			w.fs.symlink(testPlist, "/tmp/x.plist")
		}, ReasonSymlink},
		{"plist not root-owned", "", 0, func(w *world) { w.fs.chown(testPlist, 501, 20) }, ReasonPlistPermissions},
		{"plist group-writable", "", 0, func(w *world) { w.fs.chmod(testPlist, 0o664) }, ReasonPlistPermissions},
		{"LaunchDaemons writable", "", 0, func(w *world) { w.fs.chmod(launchDaemonsDir, 0o777) }, ReasonPlistPermissions},
		{"BundleProgram job", "", 0, func(w *world) {
			w.fs.rewrite(testPlist, daemonPlist(testLabel, testProgram, "<key>BundleProgram</key><string>Contents/MacOS/x</string>"))
		}, ReasonBundleProgram},
		{"duplicate in /System", "", 0, func(w *world) {
			w.fs.file(systemLaunchDaemonsDir+"/dup.plist", daemonPlist(testLabel, "/usr/libexec/x", ""), 0, 0, 0o644)
		}, ReasonDuplicateLabel},
		{"duplicate read by partial scan", "", 0, func(w *world) {
			big := daemonPlist(testLabel, "/opt/x", "<key>Pad</key><string>"+strings.Repeat("a", maxPlistBytes)+"</string>")
			w.fs.file(launchDaemonsDir+"/big.plist", big, 0, 0, 0o644)
		}, ReasonDuplicateLabel},
		{"unreadable candidate plist", "", 0, func(w *world) {
			w.fs.file(systemLaunchDaemonsDir+"/broken.plist", "<plist><dict><key>", 0, 0, 0o644)
		}, ReasonUnreadablePlist},
		{"candidate plist is a directory", "", 0, func(w *world) { w.fs.dir(launchDaemonsDir+"/odd.plist", 0, 0, 0o755) }, ReasonUnreadablePlist},
		{"program under /usr", "", 0, func(w *world) {
			w.fs.rewrite(testPlist, daemonPlist(testLabel, "/usr/LOCAL/bin/agentd", ""))
		}, ReasonSystemPath},
		{"program path symlink", "", 0, func(w *world) {
			w.fs.symlink(testBundle+"/Contents/Library/LaunchServices/agentd", "/opt/agentd")
			w.fs.file("/opt/agentd", "bin", 0, 0, 0o755)
		}, ReasonSymlink},
		{"program outside a bundle", "", 0, func(w *world) {
			w.fs.file("/opt/agentd", "bin", 0, 0, 0o755)
			w.fs.rewrite(testPlist, daemonPlist(testLabel, "/opt/agentd", ""))
		}, ReasonProgramNotInBundle},
		{"helper tool without associated app", "", 0, func(w *world) {
			w.fs.file(privilegedHelperToolsDir+"/com.example.agentd", "bin", 0, 0, 0o755)
			w.fs.rewrite(testPlist, daemonPlist(testLabel, privilegedHelperToolsDir+"/com.example.agentd", ""))
		}, ReasonProgramNotInBundle},
		{"program owned by a user", "", 0, func(w *world) { w.fs.chown(testProgram, 501, 20) }, ReasonProgramWritable},
		{"bundle dir owned by a user", "", 0, func(w *world) { w.fs.chown(testBundle, 501, 80) }, ReasonProgramWritable},
		{"program group-writable", "", 0, func(w *world) { w.fs.chmod(testProgram, 0o775) }, ReasonProgramWritable},
		{"ancestor world-writable", "", 0, func(w *world) { w.fs.chmod(testBundle+"/Contents", 0o777) }, ReasonProgramWritable},
		{"bundle not signed", "", 0, func(w *world) { w.host.unsigned[testBundle] = true }, ReasonUnsigned},
		{"ad hoc signature", "", 0, func(w *world) { w.host.adhoc = true }, ReasonUnsigned},
		{"program signed by another team", "", 0, func(w *world) { w.host.wrongTeam[testProgram] = true }, ReasonTeamMismatch},
		{"apple-signed program", "", 0, func(w *world) { w.host.appleSigned[testProgram] = true }, ReasonAppleSigned},
		{"apple bundle id", "", 0, func(w *world) { w.fs.rewrite(testBundle+"/Contents/Info.plist", infoPlist("com.apple.Example")) }, ReasonAppleLabel},
		{"launchd runs another plist", "", 0, func(w *world) { w.host.jobPath = "/Library/LaunchDaemons/elsewhere.plist" }, ReasonLaunchdMismatch},
		{"launchd runs another program", "", 0, func(w *world) { w.host.jobProgram = "/opt/evil" }, ReasonLaunchdMismatch},
		{"managed by Label rule", "", 0, func(w *world) { writeSM(w, managedPrefsDir, "Label", testLabel) }, ReasonManaged},
		{"managed by LabelPrefix rule", "", 0, func(w *world) { writeSM(w, managedPrefsDir, "LabelPrefix", "com.example.") }, ReasonManaged},
		{"managed by BundleIdentifier rule", "", 0, func(w *world) { writeSM(w, managedPrefsDir+"/alice", "BundleIdentifier", "com.example.app") }, ReasonManaged},
		{"managed by BundleIdentifierPrefix rule", "", 0, func(w *world) { writeSM(w, managedPrefsDir, "BundleIdentifierPrefix", "com.example") }, ReasonManaged},
		{"managed by TeamIdentifier rule", "", 0, func(w *world) { writeSM(w, managedPrefsDir, "TeamIdentifier", testTeam) }, ReasonManaged},
		{"BTM flags managed", "", 0, func(w *world) {
			w.host.btm = strings.Replace(testBTM, "[ legacy ] (0x1)", "[ legacy, managed ] (0x9)", 1)
		}, ReasonManaged},
		{"BTM unknown flag", "", 0, func(w *world) { w.host.btm = strings.Replace(testBTM, "[ legacy ] (0x1)", "[ novel ] (0x100)", 1) }, ReasonManaged},
		{"symlinked payload", "", 0, func(w *world) {
			w.fs.dir(managedPrefsDir, 0, 0, 0o755)
			w.fs.file("/tmp/sm.plist", "<plist><dict/></plist>", 0, 0, 0o644)
			w.fs.symlink(managedPrefsDir+"/"+serviceManagementPlist, "/tmp/sm.plist")
		}, ReasonManagementUnknown},
		{"malformed payload", "", 0, func(w *world) {
			w.fs.dir(managedPrefsDir, 0, 0, 0o755)
			w.fs.file(managedPrefsDir+"/"+serviceManagementPlist, "not a plist", 0, 0, 0o644)
		}, ReasonManagementUnknown},
		{"unknown RuleType", "", 0, func(w *world) { writeSM(w, managedPrefsDir, "Mystery", "x") }, ReasonManagementUnknown},
		{"sfltool fails", "", 0, func(w *world) { w.host.btmExit = 1 }, ReasonManagementUnknown},
		{"sfltool output unparseable", "", 0, func(w *world) { w.host.btm = "garbage" }, ReasonManagementUnknown},
		{"BTM record without Flags", "", 0, func(w *world) {
			w.host.btm = strings.Replace(testBTM, "                Flags: [ legacy ] (0x1)\n", "", 1)
		}, ReasonManagementUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			if tc.setup != nil {
				tc.setup(w)
			}
			label := tc.label
			if label == "" {
				label = testLabel
			}
			for _, action := range []string{"disable", "enable"} {
				p := w.prepare(tc.uid, label, action)
				if !p.Refused || p.Reason != tc.reason || p.Token != "" {
					t.Fatalf("%s: refused=%v reason=%q (want %q) message=%q", action, p.Refused, p.Reason, tc.reason, p.Message)
				}
			}
			if m := w.host.mutations(); len(m) != 0 {
				t.Fatalf("refusal ran launchctl mutations: %v", m)
			}
			if w.svc.tokens.len() != 0 {
				t.Fatal("refusal stored a token")
			}
		})
	}
}

func writeDenylist(w *world, labels, teams string) {
	w.fs.dir(DenylistDir, 0, 0, 0o755)
	w.fs.file(DenylistPath, `<?xml version="1.0"?><plist version="1.0"><dict><key>Labels</key><array><!-- why -->`+labels+
		`</array><key>TeamIDs</key><array>`+teams+`</array><key>Future</key><true/></dict></plist>`, 0, 0, 0o644)
}

func writeSM(w *world, dir, typ, val string) {
	w.fs.dir(managedPrefsDir, 0, 0, 0o755)
	w.fs.dir(dir, 0, 0, 0o755)
	w.fs.file(filepath.Join(dir, serviceManagementPlist), `<?xml version="1.0"?><plist version="1.0"><dict><key>Rules</key><array><dict><key>RuleType</key><string>`+
		typ+`</string><key>RuleValue</key><string>`+val+`</string></dict></array></dict></plist>`, 0, 0, 0o644)
}

func TestLaunchdPassesWithBenignPolicyAndClutter(t *testing.T) {
	w := newWorld(t)
	writeDenylist(w, "<string>com.other.*</string>", "<string>ZZZZZ99999</string>")
	writeSM(w, managedPrefsDir, "Label", "com.other.thing")
	w.fs.file(systemLaunchDaemonsDir+"/.hidden.plist", "garbage", 0, 0, 0o644)
	w.fs.file(systemLaunchDaemonsDir+"/notes.txt", "garbage", 0, 0, 0o644)
	w.host.detritus[testBundle] = true // --strict rejects only Finder metadata: retried without it
	if p := w.prepare(501, testLabel, "disable"); p.Refused {
		t.Fatalf("refused: %s %s", p.Reason, p.Message)
	}
}

func TestLaunchdKnownBTMFlagsPass(t *testing.T) {
	for _, flags := range []string{"[  ] (0)", "[ legacy, curated ] (0x5)", "[ sandboxed ] (0x40)"} {
		w := newWorld(t)
		w.host.btm = strings.Replace(testBTM, "[ legacy ] (0x1)", flags, 1)
		if p := w.prepare(501, testLabel, "disable"); p.Refused {
			t.Fatalf("%s: refused %s %s", flags, p.Reason, p.Message)
		}
	}
}

func TestLaunchdHelperToolOwnedByAssociatedApp(t *testing.T) {
	w := newWorld(t)
	tool := privilegedHelperToolsDir + "/com.example.agentd"
	w.fs.file(tool, "bin", 0, 0, 0o544)
	w.fs.rewrite(testPlist, daemonPlist(testLabel, tool, "<key>AssociatedBundleIdentifiers</key><string>com.example.app</string>"))
	w.host.set(func(h *fakeHost) { h.jobProgram = tool })
	p := w.prepare(501, testLabel, "disable")
	if p.Refused || p.Target.BundlePath != testBundle || p.Target.Program != tool {
		t.Fatalf("result = %+v %+v", p, p.Target)
	}
}

func TestLaunchdDenylistAbsentWarns(t *testing.T) {
	w := newWorld(t)
	w.prepare(501, testLabel, "disable")
	if !strings.Contains(w.audit.String(), `"outcome":"warning"`) {
		t.Fatalf("no warning for a missing denylist:\n%s", w.audit.String())
	}
}

func TestDefaultDenylistParses(t *testing.T) {
	var d denylist
	if err := d.parse([]byte(DefaultDenylistPlist)); err != nil {
		t.Fatal(err)
	}
	if d.matchLabel(HelperLabel) == nil || d.matchLabel("com.crowdstrike.falcon.Agent") == nil || d.matchLabel(testLabel) != nil {
		t.Fatal("default denylist does not match as expected")
	}
}

// --- token ---

func TestLaunchdTokenSingleUse(t *testing.T) {
	w := newWorld(t)
	p := w.prepare(501, testLabel, "disable")
	w.execute(501, "disable", p.Token)
	n := len(w.host.mutations())
	r := w.execute(501, "disable", p.Token)
	if r.Reason != ReasonTokenInvalid || len(w.host.mutations()) != n {
		t.Fatalf("replay: %+v", r)
	}
	if !strings.Contains(w.audit.String(), tokenHashPrefix(p.Token)) || strings.Contains(w.audit.String(), p.Token) {
		t.Fatal("replay audit must carry the hash, never the token")
	}
}

func TestLaunchdTokenExpiry(t *testing.T) {
	w := newWorld(t)
	p := w.prepare(501, testLabel, "disable")
	w.now = w.now.Add(tokenTTL)
	if r := w.execute(501, "disable", p.Token); r.Reason != ReasonTokenExpired {
		t.Fatalf("reason = %q", r.Reason)
	}
	if r := w.execute(501, "disable", p.Token); r.Reason != ReasonTokenInvalid {
		t.Fatalf("expired token not deleted: %q", r.Reason)
	}
	assertNoMutation(t, w)
}

func TestLaunchdTokenBoundToUIDAndAction(t *testing.T) {
	w := newWorld(t)
	w.admin[600] = true
	p := w.prepare(501, testLabel, "disable")
	if r := w.execute(600, "disable", p.Token); r.Reason != ReasonTokenInvalid {
		t.Fatalf("other uid: %q", r.Reason)
	}
	p = w.prepare(501, testLabel, "disable")
	if r := w.execute(501, "enable", p.Token); r.Reason != ReasonTokenInvalid {
		t.Fatalf("other action: %q", r.Reason)
	}
	if r := w.execute(501, "disable", p.Token); r.Reason != ReasonTokenInvalid {
		t.Fatalf("token survived a failed use: %q", r.Reason)
	}
	assertNoMutation(t, w)
}

func TestLaunchdTokenEvictsOldestAtFive(t *testing.T) {
	w := newWorld(t)
	var tokens []string
	for range 5 {
		tokens = append(tokens, w.prepare(501, testLabel, "disable").Token)
		w.now = w.now.Add(time.Second)
	}
	if r := w.execute(501, "disable", tokens[0]); r.Reason != ReasonTokenInvalid {
		t.Fatalf("oldest not evicted: %q", r.Reason)
	}
	if w.svc.tokens.len() != 4 {
		t.Fatalf("live tokens = %d", w.svc.tokens.len())
	}
	if r := w.execute(501, "disable", tokens[4]); r.Outcome != OutcomeOK {
		t.Fatalf("newest token: %+v", r)
	}
}

func TestLaunchdTargetChanged(t *testing.T) {
	cases := []struct {
		name   string
		action string
		loaded bool
		change func(w *world)
	}{
		{"plist bytes", "disable", true, func(w *world) {
			w.fs.rewrite(testPlist, daemonPlist(testLabel, testProgram, "<key>RunAtLoad</key><true/>"))
		}},
		{"plist inode", "disable", true, func(w *world) { w.fs.file(testPlist, daemonPlist(testLabel, testProgram, ""), 0, 0, 0o644) }},
		{"program", "disable", true, func(w *world) {
			other := testBundle + "/Contents/Library/LaunchServices/agentd2"
			w.fs.file(other, "bin", 0, 0, 0o755)
			w.fs.rewrite(testPlist, daemonPlist(testLabel, other, ""))
			w.host.jobProgram = other
		}},
		{"team id", "disable", true, func(w *world) { w.host.team = "ZYXWV98765" }},
		{"program bytes rewritten in place", "disable", true, func(w *world) { w.fs.rewrite(testProgram, "\xcf\xfa\xed\xfe\x07") }},
		{"program replaced (new inode)", "disable", true, func(w *world) { w.fs.file(testProgram, "\xcf\xfa\xed\xfe", 0, 0, 0o755) }},
		{"program cdhash", "disable", true, func(w *world) { w.host.cdhash[testProgram] = "fedcba9876543210fedcba9876543210fedcba98" }},
		{"disable: loaded after prepare", "disable", false, func(w *world) { w.host.loaded, w.host.pid = true, 9 }},
		{"enable: loaded after prepare", "enable", false, func(w *world) { w.host.loaded, w.host.pid = true, 9 }},
		{"enable: unloaded after prepare", "enable", true, func(w *world) { w.host.loaded = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.host.set(func(h *fakeHost) { h.loaded = tc.loaded })
			p := w.prepare(501, testLabel, tc.action)
			if p.Refused {
				t.Fatalf("prepare refused: %s", p.Message)
			}
			w.host.set(func(*fakeHost) {})
			tc.change(w)
			r := w.execute(501, tc.action, p.Token)
			if r.Reason != ReasonTargetChanged {
				t.Fatalf("reason = %q (%s)", r.Reason, r.Message)
			}
			assertNoMutation(t, w)
		})
	}
}

func TestLaunchdExecuteRerunsChecks(t *testing.T) {
	w := newWorld(t)
	p := w.prepare(501, testLabel, "disable")
	writeSM(w, managedPrefsDir, "Label", testLabel)
	if r := w.execute(501, "disable", p.Token); r.Reason != ReasonManaged {
		t.Fatalf("reason = %q", r.Reason)
	}
	p = w.prepare(501, testLabel, "enable")
	if !p.Refused {
		t.Fatal("managed daemon prepared")
	}
	w2 := newWorld(t)
	p = w2.prepare(501, testLabel, "enable")
	w2.fs.chown(testProgram, 501, 20)
	if r := w2.execute(501, "enable", p.Token); r.Reason != ReasonProgramWritable {
		t.Fatalf("program made writable after prepare: %q", r.Reason)
	}
	assertNoMutation(t, w)
	assertNoMutation(t, w2)
}

func TestLaunchdExecuteRefusesNonAdmin(t *testing.T) {
	w := newWorld(t)
	p := w.prepare(501, testLabel, "disable")
	w.admin[501] = false
	if r := w.execute(501, "disable", p.Token); r.Reason != ReasonNotAdmin {
		t.Fatalf("reason = %q", r.Reason)
	}
	assertNoMutation(t, w)
}

func assertNoMutation(t *testing.T, w *world) {
	t.Helper()
	if m := w.host.mutations(); len(m) != 0 {
		t.Fatalf("mutations ran: %v", m)
	}
}

// --- execution edge cases ---

func TestLaunchdEnableSkipsBootstrapWhenLoadedMeanwhile(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) { h.loaded, h.disabled = false, true })
	p := w.prepare(501, testLabel, "enable")
	w.host.set(func(h *fakeHost) {
		h.hook = func(argv []string) (proc.Result, bool) {
			if len(argv) > 1 && argv[1] == "enable" {
				h.disabled, h.loaded, h.pid = false, true, 5
				return proc.Result{}, true
			}
			return proc.Result{}, false
		}
	})
	r := w.execute(501, "enable", p.Token)
	if r.Outcome != OutcomeOK || len(r.Skipped) != 1 || r.Skipped[0][1] != "bootstrap" {
		t.Fatalf("result = %+v", r)
	}
	if got := strings.Join(w.host.mutations(), ";"); got != "enable system/com.example.agentd" {
		t.Fatalf("mutations = %s", got)
	}
}

func TestLaunchdEnableStopsWhenAnotherJobLoadedMeanwhile(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) { h.loaded, h.disabled = false, true })
	p := w.prepare(501, testLabel, "enable")
	w.host.set(func(h *fakeHost) {
		h.hook = func(argv []string) (proc.Result, bool) {
			if len(argv) > 1 && argv[1] == "enable" {
				h.disabled, h.loaded, h.jobPath = false, true, "/Library/LaunchDaemons/impostor.plist"
				return proc.Result{}, true
			}
			return proc.Result{}, false
		}
	})
	r := w.execute(501, "enable", p.Token)
	if r.Reason != ReasonLaunchdMismatch || r.Outcome != OutcomeMismatch {
		t.Fatalf("result = %+v", r)
	}
	if got := strings.Join(w.host.mutations(), ";"); got != "enable system/com.example.agentd" {
		t.Fatalf("mutations = %s", got)
	}
}

func TestLaunchdEnableBootstrapFails(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) {
		h.loaded, h.disabled = false, true
		h.hook = func(argv []string) (proc.Result, bool) {
			if len(argv) > 1 && argv[1] == "bootstrap" {
				return proc.Result{ExitCode: 5, Stderr: []byte("Bootstrap failed: 5: Input/output error")}, true
			}
			return proc.Result{}, false
		}
	})
	p := w.prepare(501, testLabel, "enable")
	r := w.execute(501, "enable", p.Token)
	if r.Outcome != OutcomeError || !strings.Contains(r.Message, "enabled but not loaded") || r.Pending ||
		len(r.Suggested) == 0 || !strings.Contains(r.Suggested[0], "bootstrap system "+testPlist) {
		t.Fatalf("result = %+v", r)
	}
	if got := strings.Join(w.host.mutations(), ";"); got != "enable system/com.example.agentd;bootstrap system "+testPlist {
		t.Fatalf("mutations = %s (no automatic re-disable)", got)
	}
}

func TestLaunchdEnablePostBootstrapMismatch(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) {
		h.loaded, h.disabled = false, true
		h.hook = func(argv []string) (proc.Result, bool) {
			if len(argv) > 1 && argv[1] == "bootstrap" {
				h.loaded, h.jobProgram = true, "/opt/other"
				return proc.Result{}, true
			}
			return proc.Result{}, false
		}
	})
	p := w.prepare(501, testLabel, "enable")
	r := w.execute(501, "enable", p.Token)
	if r.Outcome != OutcomeMismatch || r.Mismatch == "" {
		t.Fatalf("result = %+v", r)
	}
	if strings.Contains(strings.Join(w.host.mutations(), ";"), "bootout") {
		t.Fatal("mismatch must not be undone with bootout")
	}
}

func TestLaunchdEnableReportsLoadedNotRunning(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) {
		h.loaded, h.disabled = false, true
		h.hook = func(argv []string) (proc.Result, bool) {
			if len(argv) > 1 && argv[1] == "bootstrap" {
				h.loaded, h.pid = true, 0 // an on-demand job: loaded, no process yet
				return proc.Result{}, true
			}
			if len(argv) > 2 && argv[1] == "print" && h.loaded && h.pid == 0 {
				return proc.Result{Stdout: []byte("system/" + h.label + " = {\n\tpath = " + h.jobPath + "\n\tstate = not running\n\tprogram = " + h.jobProgram + "\n}\n")}, true
			}
			return proc.Result{}, false
		}
	})
	p := w.prepare(501, testLabel, "enable")
	r := w.execute(501, "enable", p.Token)
	if r.Outcome != OutcomeOK || !r.After.Loaded || r.After.Running {
		t.Fatalf("result = %+v after %+v", r, r.After)
	}
}

func TestLaunchdDisableBootoutFails(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) {
		h.hook = func(argv []string) (proc.Result, bool) {
			if len(argv) > 1 && argv[1] == "bootout" {
				return proc.Result{ExitCode: 5, Stderr: []byte("Boot-out failed: 5: Input/output error")}, true
			}
			return proc.Result{}, false
		}
	})
	p := w.prepare(501, testLabel, "disable")
	r := w.execute(501, "disable", p.Token)
	if r.Outcome != OutcomeError || !strings.Contains(r.Message, "disabled but still running") {
		t.Fatalf("result = %+v", r)
	}
}

func TestLaunchdDisableDoesNotAddBootout(t *testing.T) {
	w := newWorld(t)
	w.host.set(func(h *fakeHost) {
		h.loaded = false
		h.hook = func(argv []string) (proc.Result, bool) {
			if len(argv) > 1 && argv[1] == "disable" {
				h.disabled, h.loaded, h.pid = true, true, 3 // loads right after the checks
				return proc.Result{}, true
			}
			return proc.Result{}, false
		}
	})
	p := w.prepare(501, testLabel, "disable")
	r := w.execute(501, "disable", p.Token)
	if got := strings.Join(w.host.mutations(), ";"); got != "disable system/com.example.agentd" {
		t.Fatalf("mutations = %s", got)
	}
	if r.Outcome != OutcomeMismatch || !strings.Contains(r.Mismatch, "disabled but running") {
		t.Fatalf("result = %+v", r)
	}
}

// --- rate limits, mutex, dispatcher wiring ---

func TestLaunchdPrepareRateLimit(t *testing.T) {
	w := newWorld(t)
	for i := range 30 {
		if _, err := w.svc.handlePrepare(501, json.RawMessage(`{"label":"com.example.missing","action":"disable"}`)); err != nil {
			t.Fatalf("prepare %d: %v", i, err)
		}
	}
	_, err := w.svc.handlePrepare(501, json.RawMessage(`{"label":"com.example.missing","action":"disable"}`))
	var re *rpcError
	if !errors.As(err, &re) || re.code != rpcRateLimited {
		t.Fatalf("31st prepare: %v", err)
	}
	if _, err := w.svc.handlePrepare(0, json.RawMessage(`{"label":"com.example.missing","action":"disable"}`)); err != nil {
		t.Fatalf("other uid limited: %v", err)
	}
	w.now = w.now.Add(time.Minute)
	if _, err := w.svc.handlePrepare(501, json.RawMessage(`{"label":"com.example.missing","action":"disable"}`)); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestLaunchdChangeRateLimitCoversBothMethods(t *testing.T) {
	w := newWorld(t)
	for i := range 10 {
		action := []string{"disable", "enable"}[i%2]
		if _, err := w.svc.execute(context.Background(), 501, action, "bogus"); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	_, err := w.svc.execute(context.Background(), 501, "enable", "bogus")
	var re *rpcError
	if !errors.As(err, &re) || re.code != rpcRateLimited {
		t.Fatalf("11th change: %v", err)
	}
	w.now = w.now.Add(10 * time.Minute)
	if _, err := w.svc.execute(context.Background(), 501, "enable", "bogus"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestLaunchdOneChangeInFlight(t *testing.T) {
	w := newWorld(t)
	p := w.prepare(501, testLabel, "disable")
	w.svc.changeMu.Lock()
	_, err := w.svc.execute(context.Background(), 501, "disable", p.Token)
	w.svc.changeMu.Unlock()
	var re *rpcError
	if !errors.As(err, &re) || re.code != rpcRateLimited {
		t.Fatalf("concurrent change: %v", err)
	}
	assertNoMutation(t, w)
}

func TestLaunchdDispatcherErrorCodes(t *testing.T) {
	w := newWorld(t)
	for _, tc := range []struct {
		params string
		code   int
	}{
		{`{"label":"com.example.agentd","action":"disable","extra":1}`, rpcInvalidParams},
		{`{"label":"../../etc/x","action":"disable"}`, rpcInvalidParams},
		{`{"label":"com.example.agentd","action":"kickstart"}`, rpcInvalidParams},
	} {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": MethodLaunchdPrepare, "params": json.RawMessage(tc.params)})
		resp := w.d.handle(501, raw)
		if resp.Error == nil || resp.Error.Code != tc.code {
			t.Fatalf("%s: %+v", tc.params, resp.Error)
		}
	}
	if _, err := decodePrepareParams([]byte(`{"label":"com.example.agentd","action":"disable"} {}`)); err == nil {
		t.Fatal("trailing data accepted")
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": MethodLaunchdDisable, "params": map[string]any{"token": "x", "label": "com.example.agentd"}})
	if resp := w.d.handle(501, raw); resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("execute must take only the token: %+v", resp.Error)
	}
	assertNoMutation(t, w)
}

func TestLaunchdUnsupportedOffDarwin(t *testing.T) {
	d := NewDispatcher()
	svc := registerLaunchd(d, LaunchdOptions{OS: hostos.Linux, Runner: proc.NewFake(), FS: newFakeFS()})
	if _, err := svc.handlePrepare(0, json.RawMessage(`{"label":"com.example.agentd","action":"disable"}`)); !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterAllMethodAllowlist(t *testing.T) {
	d := NewDispatcher()
	RegisterAll(d, func(string, ...string) ([]byte, error) { return nil, nil })
	var got []string
	for m := range d.handlers {
		got = append(got, m)
	}
	want := map[string]bool{
		"helper.health": true, "helper.powermetrics.sample": true, "helper.process.tree": true, "helper.firewall.rules": true,
		"helper.fs_usage.start": true, "helper.fs_usage.stop": true, "helper.net_capture.start": true, "helper.net_capture.stop": true,
		"helper.tcc.system.query": true, MethodLaunchdPrepare: true, MethodLaunchdDisable: true, MethodLaunchdEnable: true,
	}
	if len(got) != len(want) {
		t.Fatalf("methods = %v", got)
	}
	for _, m := range got {
		if !want[m] {
			t.Fatalf("unexpected method %s", m)
		}
	}
}

// The mutation methods are reachable only from the local CLI: no MCP,
// per-user daemon or Remote code names them.
func TestLaunchdMethodsNotExposedElsewhere(t *testing.T) {
	for _, dir := range []string{"../mcp", "../daemon", "../daemonclient", "../fleet", "../../cmd/spectra-mcp"} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, walkErr error) error {
			if walkErr != nil {
				t.Errorf("walk %s: %v", p, walkErr)
				return nil
			}
			if e.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			data, err := os.ReadFile(p) // #nosec G304 -- test reads repo sources
			if err != nil {
				t.Errorf("read %s: %v", p, err)
				return nil
			}
			s := string(data)
			if strings.Contains(s, "helper.launchd") || strings.Contains(s, "LaunchdSystem") || strings.Contains(s, "MethodLaunchd") {
				t.Errorf("%s references the helper launchd methods", p)
			}
			return nil
		})
	}
}

func TestParseLaunchctlPrintIgnoresNestedPaths(t *testing.T) {
	out := "system/com.example.agentd = {\n\tactive count = 1\n\tsockets = {\n\t\t\"Listeners\" = {\n\t\t\tpath = /var/run/evil.sock\n\t\t}\n\t}\n" +
		"\tpath = /Library/LaunchDaemons/com.example.agentd.plist\n\tstate = running\n\targuments = {\n\t\t/opt/a\n\t\t--flag\n\t}\n\tpid = 42\n}\n"
	j := parseLaunchctlPrint([]byte(out))
	if j.path != "/Library/LaunchDaemons/com.example.agentd.plist" || j.prog() != "/opt/a" || j.pid != 42 || j.state != "running" {
		t.Fatalf("job = %+v", j)
	}
}

func TestDsmemberAdmin(t *testing.T) {
	fake := proc.NewFake().
		OnExact(dsmemberutilBin, []string{"checkmembership", "-u", "501", "-G", "admin"}, proc.Response{Result: proc.Result{Stdout: []byte("user is a member of the group\n")}}).
		OnExact(dsmemberutilBin, []string{"checkmembership", "-u", "502", "-G", "admin"}, proc.Response{Result: proc.Result{Stdout: []byte("user is not a member of the group\n")}}).
		OnExact(dsmemberutilBin, []string{"checkmembership", "-u", "503", "-G", "admin"}, proc.Response{Result: proc.Result{Stdout: []byte("maybe\n")}})
	is := dsmemberAdmin(fake)
	if ok, err := is(context.Background(), 501); !ok || err != nil {
		t.Fatalf("501: %v %v", ok, err)
	}
	if ok, err := is(context.Background(), 502); ok || err != nil {
		t.Fatalf("502: %v %v", ok, err)
	}
	if ok, err := is(context.Background(), 503); ok || err == nil {
		t.Fatalf("503 must fail closed: %v %v", ok, err)
	}
}

func FuzzLaunchdParams(f *testing.F) {
	for _, s := range []string{`{"label":"com.example.agentd","action":"disable"}`, `{"token":"abc"}`, `{}`, `null`, `[]`, `{"label":1}`, `{"token":"a"} x`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if p, err := decodePrepareParams(data); err == nil {
			if !validLabel(p.Label) || (p.Action != "disable" && p.Action != "enable") {
				t.Fatalf("accepted %q", data)
			}
			if _, ok := plistPathFor(p.Label); !ok {
				t.Fatalf("valid label %q has no plist path", p.Label)
			}
		}
		if p, err := decodeExecuteParams(MethodLaunchdDisable, data); err == nil && (p.Token == "" || len(p.Token) > 64) {
			t.Fatalf("accepted token %q", p.Token)
		}
	})
}
