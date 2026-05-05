package main

import "testing"

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
