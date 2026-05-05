// Package diff computes a structured diff between two Spectra snapshots.
// Each category uses a category-specific equality rule as described in
// docs/design/system-inventory.md#diff-semantics.
package diff

import (
	"fmt"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// ChangeKind classifies one diff entry.
type ChangeKind string

const (
	Added   ChangeKind = "added"
	Removed ChangeKind = "removed"
	Changed ChangeKind = "changed"
)

// Change is one leaf difference within a category.
type Change struct {
	Kind   ChangeKind
	Key    string // identity key (bundle ID, formula name, port string, sysctl key, …)
	Before string // empty for Added
	After  string // empty for Removed
}

// Section groups changes under one logical category.
type Section struct {
	Name    string
	Changes []Change
}

// Result holds the full diff output between two snapshots.
type Result struct {
	AID      string
	BID      string
	Sections []Section
}

// HasChanges reports whether any section contains at least one change.
func (r Result) HasChanges() bool {
	for _, s := range r.Sections {
		if len(s.Changes) > 0 {
			return true
		}
	}
	return false
}

// Compare computes the structural diff of snapshots a and b. Sections with
// zero changes are included so callers know which categories were evaluated.
func Compare(a, b snapshot.Snapshot) Result {
	return Result{
		AID: a.ID,
		BID: b.ID,
		Sections: []Section{
			diffHost(a, b),
			diffApps(a, b),
			diffJDKs(a, b),
			diffBrewFormulae(a, b),
			diffActiveRuntimes(a, b),
			diffListeningPorts(a, b),
			diffVPN(a, b),
			diffPathDirs(a, b),
			diffSysctls(a, b),
		},
	}
}

// diffHost compares HostInfo fields one-by-one.
func diffHost(a, b snapshot.Snapshot) Section {
	type field struct {
		key    string
		before string
		after  string
	}
	fields := []field{
		{"hostname", a.Host.Hostname, b.Host.Hostname},
		{"os_version", a.Host.OSVersion, b.Host.OSVersion},
		{"os_build", a.Host.OSBuild, b.Host.OSBuild},
		{"cpu_brand", a.Host.CPUBrand, b.Host.CPUBrand},
		{"cpu_cores", fmt.Sprint(a.Host.CPUCores), fmt.Sprint(b.Host.CPUCores)},
		{"ram_bytes", fmt.Sprint(a.Host.RAMBytes), fmt.Sprint(b.Host.RAMBytes)},
		{"arch", a.Host.Architecture, b.Host.Architecture},
		{"spectra_version", a.Host.SpectraVersion, b.Host.SpectraVersion},
	}
	var changes []Change
	for _, f := range fields {
		if f.before != f.after {
			changes = append(changes, Change{Kind: Changed, Key: f.key, Before: f.before, After: f.after})
		}
	}
	return Section{Name: "host", Changes: changes}
}

// diffApps matches by bundle ID (falling back to path when bundle ID is empty).
// Reports added, removed, version-changed, and security-changed apps.
func diffApps(a, b snapshot.Snapshot) Section {
	type appInfo struct {
		version          string
		path             string
		hardenedRuntime  bool
		gatekeeperStatus string
		teamID           string
	}

	mapA := make(map[string]appInfo, len(a.Apps))
	for _, r := range a.Apps {
		k := r.BundleID
		if k == "" {
			k = r.Path
		}
		mapA[k] = appInfo{
			version: r.AppVersion, path: r.Path,
			hardenedRuntime:  r.HardenedRuntime,
			gatekeeperStatus: r.GatekeeperStatus,
			teamID:           r.TeamID,
		}
	}
	mapB := make(map[string]appInfo, len(b.Apps))
	for _, r := range b.Apps {
		k := r.BundleID
		if k == "" {
			k = r.Path
		}
		mapB[k] = appInfo{
			version: r.AppVersion, path: r.Path,
			hardenedRuntime:  r.HardenedRuntime,
			gatekeeperStatus: r.GatekeeperStatus,
			teamID:           r.TeamID,
		}
	}

	boolStr := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}

	var changes []Change
	for k, ia := range mapA {
		ib, ok := mapB[k]
		if !ok {
			changes = append(changes, Change{Kind: Removed, Key: k, Before: ia.version})
			continue
		}
		if ia.version != ib.version {
			changes = append(changes, Change{Kind: Changed, Key: k, Before: ia.version, After: ib.version})
		}
		if ia.hardenedRuntime != ib.hardenedRuntime {
			changes = append(changes, Change{Kind: Changed, Key: k + ":hardened-runtime",
				Before: boolStr(ia.hardenedRuntime), After: boolStr(ib.hardenedRuntime)})
		}
		if ia.gatekeeperStatus != ib.gatekeeperStatus && ia.gatekeeperStatus != "" && ib.gatekeeperStatus != "" {
			changes = append(changes, Change{Kind: Changed, Key: k + ":gatekeeper",
				Before: ia.gatekeeperStatus, After: ib.gatekeeperStatus})
		}
		if ia.teamID != ib.teamID {
			before, after := ia.teamID, ib.teamID
			if before == "" {
				before = "(unsigned)"
			}
			if after == "" {
				after = "(unsigned)"
			}
			changes = append(changes, Change{Kind: Changed, Key: k + ":team-id",
				Before: before, After: after})
		}
	}
	for k, ib := range mapB {
		if _, ok := mapA[k]; !ok {
			changes = append(changes, Change{Kind: Added, Key: k, After: ib.version})
		}
	}
	return Section{Name: "apps", Changes: changes}
}

// diffJDKs matches by (major.minor.patch, vendor). Same identity at different
// paths is "compatible"; different identities are "drift".
func diffJDKs(a, b snapshot.Snapshot) Section {
	type jdkKey struct {
		major, minor, patch int
		vendor              string
	}
	key := func(j toolchain.JDKInstall) jdkKey {
		return jdkKey{j.VersionMajor, j.VersionMinor, j.VersionPatch, j.Vendor}
	}
	label := func(j toolchain.JDKInstall) string {
		return fmt.Sprintf("%d.%d.%d/%s@%s", j.VersionMajor, j.VersionMinor, j.VersionPatch, j.Vendor, j.Source)
	}

	setA := make(map[jdkKey]toolchain.JDKInstall, len(a.Toolchains.JDKs))
	for _, j := range a.Toolchains.JDKs {
		setA[key(j)] = j
	}
	setB := make(map[jdkKey]toolchain.JDKInstall, len(b.Toolchains.JDKs))
	for _, j := range b.Toolchains.JDKs {
		setB[key(j)] = j
	}

	var changes []Change
	for k, ja := range setA {
		if _, ok := setB[k]; !ok {
			changes = append(changes, Change{Kind: Removed, Key: label(ja), Before: ja.Path})
		}
	}
	for k, jb := range setB {
		if _, ok := setA[k]; !ok {
			changes = append(changes, Change{Kind: Added, Key: label(jb), After: jb.Path})
		}
	}
	return Section{Name: "jdks", Changes: changes}
}

// diffBrewFormulae matches by formula name; reports added/removed/version-changed.
func diffBrewFormulae(a, b snapshot.Snapshot) Section {
	mapA := make(map[string]string, len(a.Toolchains.Brew.Formulae))
	for _, f := range a.Toolchains.Brew.Formulae {
		mapA[f.Name] = f.Version
	}
	mapB := make(map[string]string, len(b.Toolchains.Brew.Formulae))
	for _, f := range b.Toolchains.Brew.Formulae {
		mapB[f.Name] = f.Version
	}

	var changes []Change
	for name, va := range mapA {
		if vb, ok := mapB[name]; !ok {
			changes = append(changes, Change{Kind: Removed, Key: name, Before: va})
		} else if va != vb {
			changes = append(changes, Change{Kind: Changed, Key: name, Before: va, After: vb})
		}
	}
	for name, vb := range mapB {
		if _, ok := mapA[name]; !ok {
			changes = append(changes, Change{Kind: Added, Key: name, After: vb})
		}
	}
	return Section{Name: "brew_formulae", Changes: changes}
}

// diffListeningPorts matches by (port, proto); reports added/removed.
func diffListeningPorts(a, b snapshot.Snapshot) Section {
	portKey := func(port, proto string) string { return proto + ":" + port }

	setA := make(map[string]bool)
	for _, p := range a.Network.ListeningPorts {
		setA[portKey(fmt.Sprint(p.Port), p.Proto)] = true
	}
	setB := make(map[string]bool)
	for _, p := range b.Network.ListeningPorts {
		setB[portKey(fmt.Sprint(p.Port), p.Proto)] = true
	}

	var changes []Change
	for k := range setA {
		if !setB[k] {
			changes = append(changes, Change{Kind: Removed, Key: k})
		}
	}
	for k := range setB {
		if !setA[k] {
			changes = append(changes, Change{Kind: Added, Key: k})
		}
	}
	return Section{Name: "listening_ports", Changes: changes}
}

// diffVPN reports when VPN active state changes between snapshots.
func diffVPN(a, b snapshot.Snapshot) Section {
	var changes []Change
	aActive, bActive := a.Network.VPNActive, b.Network.VPNActive
	if aActive != bActive {
		before, after := "false", "false"
		if aActive {
			before = "true"
		}
		if bActive {
			after = "true"
		}
		changes = append(changes, Change{Kind: Changed, Key: "vpn_active", Before: before, After: after})
	}
	return Section{Name: "vpn", Changes: changes}
}

// diffPathDirs sequence-compares PATH dirs (order matters for shadowing).
func diffPathDirs(a, b snapshot.Snapshot) Section {
	pa := a.Toolchains.Env.PathDirs
	pb := b.Toolchains.Env.PathDirs

	// Build indexed sets for quick lookup.
	posA := make(map[string]int, len(pa))
	for i, d := range pa {
		posA[d] = i
	}
	posB := make(map[string]int, len(pb))
	for i, d := range pb {
		posB[d] = i
	}

	var changes []Change
	for _, d := range pa {
		if _, ok := posB[d]; !ok {
			changes = append(changes, Change{Kind: Removed, Key: d, Before: fmt.Sprintf("pos %d", posA[d])})
		}
	}
	for _, d := range pb {
		if _, ok := posA[d]; !ok {
			changes = append(changes, Change{Kind: Added, Key: d, After: fmt.Sprintf("pos %d", posB[d])})
		} else if posA[d] != posB[d] {
			changes = append(changes, Change{Kind: Changed, Key: d,
				Before: fmt.Sprintf("pos %d", posA[d]),
				After:  fmt.Sprintf("pos %d", posB[d]),
			})
		}
	}
	return Section{Name: "path_dirs", Changes: changes}
}

// diffSysctls compares every key that appears in either snapshot.
func diffSysctls(a, b snapshot.Snapshot) Section {
	var changes []Change
	for k, va := range a.Sysctls {
		if vb, ok := b.Sysctls[k]; !ok {
			changes = append(changes, Change{Kind: Removed, Key: k, Before: va})
		} else if va != vb {
			changes = append(changes, Change{Kind: Changed, Key: k, Before: va, After: vb})
		}
	}
	for k, vb := range b.Sysctls {
		if _, ok := a.Sysctls[k]; !ok {
			changes = append(changes, Change{Kind: Added, Key: k, After: vb})
		}
	}
	return Section{Name: "sysctls", Changes: changes}
}

// diffActiveRuntimes compares the active version for each language runtime
// (node, python, go, ruby). A change fires when the active version differs
// between the two snapshots.
func diffActiveRuntimes(a, b snapshot.Snapshot) Section {
	type langRuntimes struct {
		name     string
		aSlice   []toolchain.RuntimeInstall
		bSlice   []toolchain.RuntimeInstall
	}
	langs := []langRuntimes{
		{"node", a.Toolchains.Node, b.Toolchains.Node},
		{"python", a.Toolchains.Python, b.Toolchains.Python},
		{"go", a.Toolchains.Go, b.Toolchains.Go},
		{"ruby", a.Toolchains.Ruby, b.Toolchains.Ruby},
	}

	activeVersion := func(runtimes []toolchain.RuntimeInstall) string {
		for _, r := range runtimes {
			if r.Active {
				return r.Version
			}
		}
		return ""
	}

	var changes []Change
	for _, l := range langs {
		va := activeVersion(l.aSlice)
		vb := activeVersion(l.bSlice)
		if va == vb {
			continue
		}
		switch {
		case va == "" && vb != "":
			changes = append(changes, Change{Kind: Added, Key: l.name, After: vb})
		case va != "" && vb == "":
			changes = append(changes, Change{Kind: Removed, Key: l.name, Before: va})
		default:
			changes = append(changes, Change{Kind: Changed, Key: l.name, Before: va, After: vb})
		}
	}
	return Section{Name: "active_runtimes", Changes: changes}
}
