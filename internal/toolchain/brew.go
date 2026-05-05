package toolchain

import (
	"encoding/json"
	"strings"
)

// discoverBrew queries Homebrew for installed formulae, casks, and taps.
// Uses the injected CmdRunner so tests can stub it without shelling out.
func discoverBrew(run CmdRunner) (BrewInventory, error) {
	var inv BrewInventory

	// Formulae from `brew info --json=v2 --installed`
	if out, err := run("brew", "info", "--json=v2", "--installed"); err == nil {
		inv.Formulae = parseBrewFormulae(out)
	}
	// Casks from `brew list --cask`
	if out, err := run("brew", "list", "--cask", "--versions"); err == nil {
		inv.Casks = parseBrewCasks(out)
	}
	// Taps from `brew tap`
	if out, err := run("brew", "tap"); err == nil {
		for _, name := range lines(out) {
			inv.Taps = append(inv.Taps, BrewTap{Name: name})
		}
	}

	if len(inv.Formulae) == 0 && len(inv.Casks) == 0 {
		return inv, nil
	}
	return inv, nil
}

// brewInfoV2 mirrors the minimal structure of `brew info --json=v2 --installed`.
type brewInfoV2 struct {
	Formulae []struct {
		Name       string `json:"name"`
		FullName   string `json:"full_name"`
		Tap        string `json:"tap"`
		Deprecated bool   `json:"deprecated"`
		Installed  []struct {
			Version            string `json:"version"`
			InstalledOnRequest bool   `json:"installed_on_request"`
		} `json:"installed"`
		Pinned bool `json:"pinned"`
	} `json:"formulae"`
}

func parseBrewFormulae(data []byte) []BrewFormula {
	var info brewInfoV2
	if err := json.Unmarshal(data, &info); err != nil {
		return nil
	}
	out := make([]BrewFormula, 0, len(info.Formulae))
	for _, f := range info.Formulae {
		ver := ""
		via := "core"
		if len(f.Installed) > 0 {
			ver = f.Installed[0].Version
		}
		// A non-empty tap that isn't homebrew/core means it's a third-party tap.
		// full_name like "user/tap/formula" also signals a tap formula.
		if f.Tap != "" && f.Tap != "homebrew/core" {
			via = "tap"
		} else if strings.Count(f.FullName, "/") >= 2 {
			// e.g. "kaeawc/tap/formula"
			via = "tap"
		}
		out = append(out, BrewFormula{
			Name:         f.Name,
			Version:      ver,
			InstalledVia: via,
			Deprecated:   f.Deprecated,
			Pinned:       f.Pinned,
		})
	}
	return out
}

// parseBrewCasks parses `brew list --cask --versions` output.
// Each line is "<name> <version>".
func parseBrewCasks(data []byte) []BrewCask {
	var out []BrewCask
	for _, line := range lines(data) {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		cask := BrewCask{Name: parts[0]}
		if len(parts) >= 2 {
			cask.Version = parts[1]
		}
		out = append(out, cask)
	}
	return out
}
