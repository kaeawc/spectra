package toolchain

import (
	"errors"
	"testing"
)

const brewInfoFixture = `{
  "formulae": [
    {
      "name": "openjdk@21",
      "full_name": "openjdk@21",
      "tap": "homebrew/core",
      "deprecated": false,
      "pinned": false,
      "installed": [{"version": "21.0.6", "installed_on_request": true}]
    },
    {
      "name": "node",
      "full_name": "node",
      "tap": "homebrew/core",
      "deprecated": false,
      "pinned": true,
      "installed": [{"version": "22.1.0", "installed_on_request": true}]
    },
    {
      "name": "old-formula",
      "full_name": "old-formula",
      "tap": "homebrew/core",
      "deprecated": true,
      "pinned": false,
      "installed": [{"version": "1.0.0", "installed_on_request": false}]
    }
  ]
}`

const brewCasksFixture = `slack 4.39.95
visual-studio-code 1.89.0
`

const brewTapsFixture = `homebrew/core
homebrew/cask
kaeawc/tap
`

func stubRunner(formulae, casks, taps []byte) CmdRunner {
	return func(name string, args ...string) ([]byte, error) {
		if name == "brew" {
			switch {
			case len(args) >= 2 && args[0] == "info":
				return formulae, nil
			case len(args) >= 1 && args[0] == "list":
				return casks, nil
			case len(args) >= 1 && args[0] == "tap":
				return taps, nil
			}
		}
		return nil, errors.New("unexpected command")
	}
}

func TestDiscoverBrewFormulae(t *testing.T) {
	runner := stubRunner([]byte(brewInfoFixture), []byte(brewCasksFixture), []byte(brewTapsFixture))
	inv, err := discoverBrew(runner)
	if err != nil {
		t.Fatal(err)
	}

	if len(inv.Formulae) != 3 {
		t.Fatalf("got %d formulae, want 3", len(inv.Formulae))
	}

	byName := map[string]BrewFormula{}
	for _, f := range inv.Formulae {
		byName[f.Name] = f
	}

	jdk := byName["openjdk@21"]
	if jdk.Version != "21.0.6" {
		t.Errorf("openjdk@21 version = %q, want 21.0.6", jdk.Version)
	}
	if jdk.InstalledVia != "core" {
		t.Errorf("openjdk@21 via = %q, want core", jdk.InstalledVia)
	}

	node := byName["node"]
	if !node.Pinned {
		t.Error("node should be pinned")
	}

	old := byName["old-formula"]
	if !old.Deprecated {
		t.Error("old-formula should be deprecated")
	}
}

func TestDiscoverBrewCasks(t *testing.T) {
	runner := stubRunner([]byte(`{"formulae":[]}`), []byte(brewCasksFixture), nil)
	inv, err := discoverBrew(runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Casks) != 2 {
		t.Fatalf("got %d casks, want 2", len(inv.Casks))
	}
	if inv.Casks[0].Name != "slack" {
		t.Errorf("cask[0].Name = %q, want slack", inv.Casks[0].Name)
	}
	if inv.Casks[0].Version != "4.39.95" {
		t.Errorf("cask[0].Version = %q, want 4.39.95", inv.Casks[0].Version)
	}
}

func TestDiscoverBrewTaps(t *testing.T) {
	runner := stubRunner([]byte(`{"formulae":[]}`), nil, []byte(brewTapsFixture))
	inv, err := discoverBrew(runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Taps) != 3 {
		t.Fatalf("got %d taps, want 3", len(inv.Taps))
	}
}

func TestDiscoverBrewCommandFails(t *testing.T) {
	// All commands fail — should return empty inventory, no error.
	runner := func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("brew not found")
	}
	inv, err := discoverBrew(runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.Formulae) != 0 || len(inv.Casks) != 0 {
		t.Errorf("expected empty inventory on failure, got %+v", inv)
	}
}
