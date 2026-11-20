package startup

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/proc"
)

const xmlHead = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
`

// padding is a value larger than the 1 MiB parse cap.
var padding = `<key>Padding</key><string>` + strings.Repeat("x", maxPlist+1) + `</string>`

func TestScanPlistLabel(t *testing.T) {
	cases := []struct {
		name, body, want string
		noLabel, fail    bool
	}{
		{name: "label first", body: `<plist><dict><key>Label</key><string>com.example.a</string>` + padding + `</dict></plist>`, want: "com.example.a"},
		{name: "label after big value", body: `<plist version="1.0"><dict>` + padding + `<key>Label</key><string>com.example.b</string></dict></plist>`, want: "com.example.b"},
		{name: "nested label ignored", body: `<plist><dict><key>Sub</key><dict><key>Label</key><string>inner</string></dict>` +
			`<key>Arr</key><array><string>Label</string></array><key>Label</key><string>outer</string></dict></plist>`, want: "outer"},
		{name: "label key as a value", body: `<plist><dict><key>K</key><string>Label</string></dict></plist>`, noLabel: true},
		{name: "empty dict", body: `<plist version="1.0"><dict/></plist>`, noLabel: true},
		{name: "empty label", body: `<plist><dict><key>Label</key><string></string></dict></plist>`, noLabel: true},
		{name: "label not a string", body: `<plist><dict><key>Label</key><integer>1</integer></dict></plist>`, fail: true},
		{name: "array root", body: `<plist><array/></plist>`, fail: true},
		{name: "not a plist", body: `<html></html>`, fail: true},
		{name: "truncated", body: `<plist><dict>` + padding[:1000], fail: true},
		{name: "binary", body: "bplist00\x00\x01", fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanPlistLabel(strings.NewReader(xmlHead + tc.body))
			if tc.name == "binary" {
				got, err = scanPlistLabel(strings.NewReader(tc.body))
			}
			switch {
			case tc.noLabel:
				if !errors.Is(err, errNoLabel) {
					t.Fatalf("got %q, %v; want errNoLabel", got, err)
				}
			case tc.fail:
				if err == nil || errors.Is(err, errNoLabel) {
					t.Fatalf("got %q, %v; want a scan error", got, err)
				}
			case err != nil || got != tc.want:
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestListScansOversizePlistLabel(t *testing.T) {
	tr := newTree(t)
	big := filepath.Join(tr.userAgents, "big.plist")
	writeFile(t, big, xmlHead+`<plist version="1.0"><dict>`+padding+`<key>Label</key><string>com.example.big</string></dict></plist>`)
	rep, err := List(context.Background(), tr.opts(newFakeLaunchd()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Path != big || rep.Errors[0].Label != "com.example.big" || !rep.Errors[0].gui {
		t.Fatalf("errors = %+v", rep.Errors)
	}
}

// TestChangeWithUnreadablePlists covers the duplicate-label check against
// plists List could not parse.
func TestChangeWithUnreadablePlists(t *testing.T) {
	cases := []struct {
		name   string
		dir    func(tree) string
		body   string
		reason string // "" when the change runs
	}{
		{name: "oversize, same label", dir: func(tr tree) string { return tr.userAgents },
			body: xmlHead + `<plist><dict>` + padding + `<key>Label</key><string>com.example.agent</string></dict></plist>`, reason: RefuseDuplicate},
		{name: "oversize, same label, system LaunchAgents", dir: func(tr tree) string { return tr.libAgents },
			body: xmlHead + `<plist><dict><key>Label</key><string>com.example.agent</string>` + padding + `</dict></plist>`, reason: RefuseDuplicate},
		{name: "oversize, other label", dir: func(tr tree) string { return tr.userAgents },
			body: xmlHead + `<plist><dict><key>Label</key><string>com.example.other</string>` + padding + `</dict></plist>`},
		{name: "oversize, no label", dir: func(tr tree) string { return tr.userAgents },
			body: xmlHead + `<plist><dict>` + padding + `</dict></plist>`},
		{name: "oversize binary", dir: func(tr tree) string { return tr.userAgents },
			body: "bplist00" + strings.Repeat("\x00", maxPlist), reason: RefuseUnreadable},
		{name: "oversize truncated", dir: func(tr tree) string { return tr.userAgents },
			body: xmlHead + `<plist><dict>` + padding, reason: RefuseUnreadable},
		{name: "broken", dir: func(tr tree) string { return tr.userAgents }, body: "<plist><dict><key>Label", reason: RefuseUnreadable},
		{name: "broken in LaunchDaemons", dir: func(tr tree) string { return tr.libDaemons }, body: "<plist><dict><key>Label"},
		{name: "empty dict", dir: func(tr tree) string { return tr.userAgents }, body: xmlHead + `<plist version="1.0"><dict/></plist>`},
		{name: "empty file", dir: func(tr tree) string { return tr.userAgents }, body: ""},
		{name: "dict without label", dir: func(tr tree) string { return tr.userAgents },
			body: xmlHead + `<plist><dict><key>Program</key><string>/opt/x</string></dict></plist>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, f, _ := changeFixture(t)
			path := filepath.Join(tc.dir(tr), "unreadable.plist")
			writeFile(t, path, tc.body)
			f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
			res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Unreadable) != 1 || res.Unreadable[0].Path != path {
				t.Fatalf("unreadable = %+v", res.Unreadable)
			}
			ran := mutatingCalls(f.Calls())
			if tc.reason != "" {
				if !res.Refused || res.Reason != tc.reason || !strings.Contains(res.Message, path) || len(ran) != 0 {
					t.Fatalf("res = %+v, ran %v", res, ran)
				}
				return
			}
			want := []string{"disable gui/501/com.example.agent", "bootout gui/501/com.example.agent"}
			if res.Refused || !reflect.DeepEqual(ran, want) || res.Mismatch != "" {
				t.Fatalf("res = %+v, ran %v", res, ran)
			}
			if !strings.Contains(res.Message, path) {
				t.Fatalf("message does not name the unreadable plist: %q", res.Message)
			}
		})
	}
}

func TestChangeNotesUnreadableAfterBootoutNote(t *testing.T) {
	tr, f, _ := changeFixture(t)
	path := filepath.Join(tr.userAgents, "empty.plist")
	writeFile(t, path, xmlHead+`<plist version="1.0"><dict/></plist>`)
	f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
	f.keepLoaded = true
	f.result["bootout"] = proc.Result{ExitCode: 36, Stderr: []byte("Boot-out failed: 36: Operation now in progress")}
	res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pending || !strings.Contains(res.Message, "bootout is in progress") || !strings.Contains(res.Message, path) {
		t.Fatalf("res = %+v", res)
	}
}
