package playbook

import (
	"strings"
	"testing"
)

func TestDefaultCatalogContainsExpectedPlaybooks(t *testing.T) {
	c := MustDefaultCatalog()
	want := []string{"jvm-memory", "network-failure", "remote-triage", "storage-bloat", "toolchain-drift"}
	got := c.List()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("playbook %d = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestDefaultCatalogDefinitionsValidate(t *testing.T) {
	for _, pb := range MustDefaultCatalog().List() {
		if err := Validate(pb); err != nil {
			t.Fatalf("Validate(%s): %v", pb.ID, err)
		}
		if len(pb.References) == 0 {
			t.Fatalf("%s has no references", pb.ID)
		}
		hasCommand := false
		for _, step := range pb.Steps {
			if len(step.Commands) > 0 {
				hasCommand = true
			}
		}
		if !hasCommand {
			t.Fatalf("%s has no commands", pb.ID)
		}
	}
}

func TestStaticCatalogRejectsDuplicateIDs(t *testing.T) {
	_, err := NewStaticCatalog([]Playbook{
		validPlaybook("same"),
		validPlaybook("same"),
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate playbook id") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRejectsEmptyCommand(t *testing.T) {
	pb := validPlaybook("bad")
	pb.Steps[0].Commands = []Command{{}}
	err := Validate(pb)
	if err == nil || !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("err = %v", err)
	}
}

func TestCatalogReturnsCopies(t *testing.T) {
	c, err := NewStaticCatalog([]Playbook{validPlaybook("one")})
	if err != nil {
		t.Fatal(err)
	}
	rows := c.List()
	rows[0].ID = "mutated"
	got, ok := c.Get("one")
	if !ok {
		t.Fatal("missing playbook one")
	}
	if got.ID != "one" {
		t.Fatalf("catalog was mutated: %+v", got)
	}
}

func validPlaybook(id string) Playbook {
	return Playbook{
		ID:      id,
		Title:   "Title",
		Symptom: "Symptom",
		Steps: []Step{
			{
				ID:       "step",
				Title:    "Step",
				Commands: []Command{{Args: []string{"jvm"}}},
			},
		},
	}
}
