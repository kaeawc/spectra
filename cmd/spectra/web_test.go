package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/asar"
)

func TestSetSubtract(t *testing.T) {
	got := setSubtract([]string{"b", "a", "c", "a"}, []string{"c"})
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("setSubtract = %v, want [a b]", got)
	}
}

func TestComputeWebAsarDiff(t *testing.T) {
	a := asarPayload{
		App:       "Slack-old",
		Archive:   &asar.Archive{Files: []asar.FileEntry{{Path: "main.js", SHA256: "aaa"}}},
		NPM:       []string{"react", "lodash"},
		Native:    []string{"node_modules/a/a.node"},
		Endpoints: []string{"api.slack.com"},
	}
	b := asarPayload{
		App:       "Slack-new",
		Archive:   &asar.Archive{Files: []asar.FileEntry{{Path: "main.js", SHA256: "bbb"}, {Path: "boot.js", SHA256: "ccc"}}},
		NPM:       []string{"react", "lodash", "node-pty"},
		Native:    []string{"node_modules/a/a.node", "node_modules/keytar/keytar.node"},
		Endpoints: []string{"api.slack.com", "telemetry.vendor.io"},
	}
	d := computeWebAsarDiff(a, b)
	if len(d.Files.Added) != 1 || d.Files.Added[0].Path != "boot.js" {
		t.Errorf("files added = %+v", d.Files.Added)
	}
	if len(d.Files.Changed) != 1 || d.Files.Changed[0].Path != "main.js" {
		t.Errorf("files changed = %+v", d.Files.Changed)
	}
	if strings.Join(d.NPMAdded, ",") != "node-pty" {
		t.Errorf("npm added = %v", d.NPMAdded)
	}
	if strings.Join(d.NativeAdded, ",") != "node_modules/keytar/keytar.node" {
		t.Errorf("native added = %v", d.NativeAdded)
	}
	if strings.Join(d.EndpointsAdded, ",") != "telemetry.vendor.io" {
		t.Errorf("endpoints added = %v", d.EndpointsAdded)
	}
}

func TestRenderWebAsarDiff(t *testing.T) {
	d := webAsarDiff{
		AppA: "old", AppB: "new",
		NPMAdded:       []string{"node-pty"},
		NativeAdded:    []string{"keytar.node"},
		EndpointsAdded: []string{"telemetry.vendor.io"},
	}
	var out bytes.Buffer
	renderWebAsarDiff(&out, d)
	s := out.String()
	for _, want := range []string{"asar-diff old -> new", "+ npm node-pty", "+ native keytar.node", "+ endpoint telemetry.vendor.io"} {
		if !strings.Contains(s, want) {
			t.Errorf("render missing %q; got:\n%s", want, s)
		}
	}
}

func TestRunWebUnknownSub(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runWebWithIO([]string{"bogus"}, &out, &errBuf, nil); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunAsarDiffWrongArgc(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runWebWithIO([]string{"asar-diff", "only-one.app"}, &out, &errBuf, nil); code != 2 {
		t.Fatalf("exit = %d, want 2 for one path", code)
	}
}
