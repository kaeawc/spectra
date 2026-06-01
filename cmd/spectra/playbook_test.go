package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/playbook"
)

type fakePlaybookCatalog struct {
	rows []playbook.Playbook
}

func (f fakePlaybookCatalog) List() []playbook.Playbook {
	return f.rows
}

func (f fakePlaybookCatalog) Get(id string) (playbook.Playbook, bool) {
	for _, row := range f.rows {
		if row.ID == id {
			return row, true
		}
	}
	return playbook.Playbook{}, false
}

func TestRunPlaybookList(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runPlaybookWith(nil, &stdout, &stderr, fakePlaybookCatalog{rows: []playbook.Playbook{
		testPlaybook(),
	}})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "jvm-memory") || !strings.Contains(out, "JVM memory") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunPlaybookShow(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runPlaybookWith([]string{"jvm-memory"}, &stdout, &stderr, fakePlaybookCatalog{rows: []playbook.Playbook{
		testPlaybook(),
	}})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "# JVM memory") || !strings.Contains(out, "$ spectra jvm <pid>") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunPlaybookCommandsOnly(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runPlaybookWith([]string{"--commands", "jvm-memory"}, &stdout, &stderr, fakePlaybookCatalog{rows: []playbook.Playbook{
		testPlaybook(),
	}})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "spectra jvm <pid>" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunPlaybookJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runPlaybookWith([]string{"--json", "jvm-memory"}, &stdout, &stderr, fakePlaybookCatalog{rows: []playbook.Playbook{
		testPlaybook(),
	}})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	var pb playbook.Playbook
	if err := json.Unmarshal(stdout.Bytes(), &pb); err != nil {
		t.Fatal(err)
	}
	if pb.ID != "jvm-memory" {
		t.Fatalf("playbook = %+v", pb)
	}
}

func TestRunPlaybookUnknown(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runPlaybookWith([]string{"missing"}, &stdout, &stderr, fakePlaybookCatalog{})
	if code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown playbook") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunPlaybookAutoFixNonInteractiveRequiresYes(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runPlaybookWith([]string{"fseventsd-leak", "--auto-fix", "--non-interactive"}, &stdout, &stderr, fakePlaybookCatalog{rows: []playbook.Playbook{
		{ID: "fseventsd-leak", Title: "fseventsd", Symptom: "memory", Steps: []playbook.Step{{ID: "s", Title: "s", Commands: []playbook.Command{{Args: []string{"memory"}}}}}},
	}})
	if code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "--auto-fix cannot be combined") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunPlaybookCommandsOnlyFSEventsdDoesNotExecute(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	pb := playbook.MustDefaultCatalog().List()[0]
	if pb.ID != "fseventsd-leak" {
		t.Fatalf("first default playbook = %s", pb.ID)
	}
	code := runPlaybookWith([]string{"--commands", "fseventsd-leak"}, &stdout, &stderr, fakePlaybookCatalog{rows: []playbook.Playbook{pb}})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "spectra memory --json") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestSubcommandListIncludesPlaybook(t *testing.T) {
	for _, sc := range subcommandList() {
		if sc.name == "playbook" {
			return
		}
	}
	t.Fatal("subcommandList missing playbook")
}

func testPlaybook() playbook.Playbook {
	return playbook.Playbook{
		ID:      "jvm-memory",
		Title:   "JVM memory",
		Symptom: "Java app slow",
		Steps: []playbook.Step{
			{
				ID:      "inspect",
				Title:   "Inspect",
				Purpose: "Inspect JVM",
				Commands: []playbook.Command{
					{Args: []string{"jvm", "<pid>"}, Description: "Inspect one JVM"},
				},
			},
		},
	}
}
