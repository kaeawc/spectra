// Package playbook defines diagnostic workflows over Spectra collectors.
package playbook

import (
	"fmt"
	"sort"
	"strings"
)

// Catalog provides named diagnostic playbooks.
type Catalog interface {
	List() []Playbook
	Get(id string) (Playbook, bool)
}

// Playbook is a problem-first diagnostic workflow.
type Playbook struct {
	ID          string
	Title       string
	Symptom     string
	Description string
	Steps       []Step
	References  []Reference
}

// Step is one diagnostic action in a playbook.
type Step struct {
	ID       string
	Title    string
	Purpose  string
	Commands []Command
	Signals  []Signal
}

// Command is a Spectra command template. Args intentionally excludes
// the leading "spectra" binary name so callers can render commands for
// local, remote, or embedded usage.
type Command struct {
	Args        []string
	Description string
	Remote      bool
	Destructive bool
}

// Signal explains how to interpret a step result.
type Signal struct {
	Name    string
	Meaning string
}

// Reference links a playbook back to collector documentation.
type Reference struct {
	Title string
	Path  string
}

// StaticCatalog is an in-memory playbook catalog.
type StaticCatalog struct {
	byID map[string]Playbook
	list []Playbook
}

// NewStaticCatalog returns a catalog from playbooks. IDs must be unique.
func NewStaticCatalog(playbooks []Playbook) (*StaticCatalog, error) {
	byID := make(map[string]Playbook, len(playbooks))
	list := make([]Playbook, 0, len(playbooks))
	for _, pb := range playbooks {
		if err := Validate(pb); err != nil {
			return nil, err
		}
		if _, exists := byID[pb.ID]; exists {
			return nil, fmt.Errorf("duplicate playbook id %q", pb.ID)
		}
		byID[pb.ID] = pb
		list = append(list, pb)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return &StaticCatalog{byID: byID, list: list}, nil
}

// List returns playbooks sorted by ID.
func (c *StaticCatalog) List() []Playbook {
	out := make([]Playbook, len(c.list))
	copy(out, c.list)
	return out
}

// Get returns a playbook by ID.
func (c *StaticCatalog) Get(id string) (Playbook, bool) {
	pb, ok := c.byID[id]
	return pb, ok
}

// Validate checks a playbook for fields required by renderers and callers.
func Validate(pb Playbook) error {
	if strings.TrimSpace(pb.ID) == "" {
		return fmt.Errorf("playbook id is required")
	}
	if strings.TrimSpace(pb.Title) == "" {
		return fmt.Errorf("playbook %q title is required", pb.ID)
	}
	if strings.TrimSpace(pb.Symptom) == "" {
		return fmt.Errorf("playbook %q symptom is required", pb.ID)
	}
	if len(pb.Steps) == 0 {
		return fmt.Errorf("playbook %q must have at least one step", pb.ID)
	}
	stepIDs := make(map[string]struct{}, len(pb.Steps))
	for _, step := range pb.Steps {
		if strings.TrimSpace(step.ID) == "" {
			return fmt.Errorf("playbook %q has a step without an id", pb.ID)
		}
		if _, exists := stepIDs[step.ID]; exists {
			return fmt.Errorf("playbook %q has duplicate step id %q", pb.ID, step.ID)
		}
		stepIDs[step.ID] = struct{}{}
		if strings.TrimSpace(step.Title) == "" {
			return fmt.Errorf("playbook %q step %q title is required", pb.ID, step.ID)
		}
		for _, cmd := range step.Commands {
			if len(cmd.Args) == 0 {
				return fmt.Errorf("playbook %q step %q has an empty command", pb.ID, step.ID)
			}
		}
	}
	return nil
}

// MustDefaultCatalog returns the built-in playbook catalog and panics if
// static definitions are invalid.
func MustDefaultCatalog() *StaticCatalog {
	c, err := NewStaticCatalog(defaultPlaybooks())
	if err != nil {
		panic(err)
	}
	return c
}
