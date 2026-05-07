package main

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
)

func TestSubcommandListIncludesList(t *testing.T) {
	for _, sc := range subcommandList() {
		if sc.name == "list" {
			return
		}
	}
	t.Fatal("subcommandList missing list alias")
}

func TestSubcommandListIncludesBaseline(t *testing.T) {
	for _, sc := range subcommandList() {
		if sc.name == "baseline" {
			return
		}
	}
	t.Fatal("subcommandList missing baseline alias")
}

func TestSubcommandListIncludesConnect(t *testing.T) {
	for _, sc := range subcommandList() {
		if sc.name == "connect" {
			return
		}
	}
	t.Fatal("subcommandList missing connect")
}

func TestListInspectArgsPrependsAll(t *testing.T) {
	got := listInspectArgs([]string{"-v", "--json"})
	want := []string{"--all", "-v", "--json"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseGlobalRemoteArgs(t *testing.T) {
	got, ok, err := parseGlobalRemoteArgs([]string{"--remote", "work-mac", "--timeout", "5s", "jvm", "thread-dump", "4012"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("remote args not detected")
	}
	if got.target != "work-mac" || got.timeout != 5*time.Second {
		t.Fatalf("remote = %+v", got)
	}
	want := []string{"jvm", "thread-dump", "4012"}
	if len(got.args) != len(want) {
		t.Fatalf("args = %v, want %v", got.args, want)
	}
	for i := range want {
		if got.args[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, got.args[i], want[i])
		}
	}
}

func TestParseGlobalRemoteArgsNormalizesDefaultInspect(t *testing.T) {
	got, ok, err := parseGlobalRemoteArgs([]string{"--target=local", "/Applications/Slack.app"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("remote args not detected")
	}
	want := []string{"inspect", "/Applications/Slack.app"}
	if len(got.args) != len(want) {
		t.Fatalf("args = %v, want %v", got.args, want)
	}
	for i := range want {
		if got.args[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, got.args[i], want[i])
		}
	}
}

func TestParseGlobalRemoteArgsIgnoresLocalFlags(t *testing.T) {
	if _, ok, err := parseGlobalRemoteArgs([]string{"--json", "/Applications/Slack.app"}); err != nil || ok {
		t.Fatalf("parseGlobalRemoteArgs detected local flags: ok=%v err=%v", ok, err)
	}
}

func TestNativeModuleLabelIncludesPackageVersion(t *testing.T) {
	got := nativeModuleLabel(detect.NativeModule{
		Name:           "addon.node",
		PackageName:    "@scope/pkg",
		PackageVersion: "1.2.3",
	})
	if got != "addon.node (@scope/pkg@1.2.3)" {
		t.Fatalf("nativeModuleLabel = %q", got)
	}
}
