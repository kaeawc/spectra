package capabilities

import protocol "github.com/kaeawc/spectra-protocol/protocol/v1"

// Result schemas owned by this repository. The protocol module names only
// inspect, snapshot and capabilities; its manifest accepts any interface and
// schema name, so the CLI-only commands are declared here. Bump a version on
// any breaking change to that command's JSON (see docs/reference/command-schemas.md).
const (
	SchemaLoad           = "spectra.load"
	SchemaOrphans        = "spectra.orphans"
	SchemaOwner          = "spectra.owner"
	SchemaOwnerUnder     = "spectra.owner.under"
	SchemaLineage        = "spectra.lineage"
	SchemaSpawns         = "spectra.spawns"
	SchemaIdle           = "spectra.idle"
	SchemaDevices        = "spectra.devices"
	SchemaStartup        = "spectra.startup"
	SchemaStartupChange  = "spectra.startup.change"
	SchemaUninstallPlan  = "spectra.uninstall.plan"
	SchemaUninstallApply = "spectra.uninstall.apply"
)

var localSchemaVersions = map[string]int{
	SchemaLoad:           1,
	SchemaOrphans:        1,
	SchemaOwner:          1,
	SchemaOwnerUnder:     1,
	SchemaLineage:        1,
	SchemaSpawns:         1,
	SchemaIdle:           1,
	SchemaDevices:        1,
	SchemaStartup:        1,
	SchemaStartupChange:  1,
	SchemaUninstallPlan:  1,
	SchemaUninstallApply: 1,
}

// LocalSchemas returns the result schemas this repository defines, with
// their current versions.
func LocalSchemas() map[string]int {
	out := make(map[string]int, len(localSchemaVersions))
	for name, v := range localSchemaVersions {
		out[name] = v
	}
	return out
}

// ResultSchemaVersion returns the current version of a protocol-owned or
// locally defined result schema.
func ResultSchemaVersion(name string) (int, bool) {
	if v, ok := protocol.SupportedResultSchemaVersion(name); ok {
		return v, true
	}
	v, ok := localSchemaVersions[name]
	return v, ok
}

type entry struct {
	name       string
	argv       []string
	schema     string
	darwinOnly bool
}

// commandEntries are the JSON commands outside the protocol's own set, in
// manifest order.
var commandEntries = []entry{
	{name: "load", schema: SchemaLoad, argv: []string{"load", "--json", "[--top <n>]", "[--root <dir>]...", "[--max-load-per-cpu <x>]", "[--min-free-gb <gib>]", "[--backoff-free-gb <gib>]", "[--sample <duration>]"}},
	{name: "orphans", schema: SchemaOrphans, argv: []string{"orphans", "--json", "[--root <dir>]...", "[--min-age <duration>]"}},
	{name: "owner", schema: SchemaOwner, argv: []string{"owner", "--json", "[--timeout <duration>]", "<target>"}},
	{name: "owner.under", schema: SchemaOwnerUnder, argv: []string{"owner", "--under", "--json", "[--timeout <duration>]", "<dir>..."}},
	{name: "lineage", schema: SchemaLineage, argv: []string{"lineage", "--json", "<pid>"}},
	{name: "spawns", schema: SchemaSpawns, argv: []string{"spawns", "--json", "[--interval <duration>]", "[--duration <duration>]", "[--pid <pid>]"}},
	{name: "idle", schema: SchemaIdle, argv: []string{"idle", "--json", "[--root <dir>]...", "[--pids <pid,...>]", "[--min-age <duration>]", "[--max-cpu <pct>]", "[--window <duration>]", "[--timeout <duration>]"}},
	{name: "devices", schema: SchemaDevices, argv: []string{"devices", "--json", "[--root <dir>]...", "[--start-adb]"}},
	{name: "startup", schema: SchemaStartup, darwinOnly: true, argv: []string{"startup", "list", "--json", "[--scope user|system|all]", "[--include-apple]"}},
	{name: "startup.disable", schema: SchemaStartupChange, darwinOnly: true, argv: []string{"startup", "disable", "<label>", "--json", "[--scope user]"}},
	{name: "startup.enable", schema: SchemaStartupChange, darwinOnly: true, argv: []string{"startup", "enable", "<label>", "--json", "[--scope user]"}},
	{name: "uninstall.plan", schema: SchemaUninstallPlan, darwinOnly: true, argv: []string{"uninstall", "plan", "--json", "<app_path|bundle_id>"}},
	{name: "uninstall.apply", schema: SchemaUninstallApply, darwinOnly: true, argv: []string{"uninstall", "apply", "--plan", "<plan_file>", "--json", "[--dry-run]", "[--quit]", "[--include-low-confidence]", "[--data-only]", "[--leftovers]"}},
}

func Build(spectraVersion, goos, goarch string) protocol.SpectraCapabilities {
	interfaces := []protocol.SpectraInterface{{Name: protocol.InterfaceVersion, Argv: []string{"version"}, Output: protocol.OutputText}}
	if goos == "darwin" {
		interfaces = append(interfaces, protocol.SpectraInterface{
			Name: protocol.InterfaceInspect, Argv: []string{"--json", "<app_path>..."}, Output: protocol.OutputJSON,
			ResultSchema: schemaRef(protocol.SchemaInspect),
		})
	}
	interfaces = append(interfaces, protocol.SpectraInterface{
		Name: protocol.InterfaceSnapshot, Argv: []string{"snapshot", "--json", "[--no-apps]"}, Output: protocol.OutputJSON,
		ResultSchema: schemaRef(protocol.SchemaSnapshot),
	})
	for _, e := range commandEntries {
		if e.darwinOnly && goos != "darwin" {
			continue
		}
		interfaces = append(interfaces, protocol.SpectraInterface{
			Name: e.name, Argv: append([]string(nil), e.argv...), Output: protocol.OutputJSON,
			ResultSchema: schemaRef(e.schema),
		})
	}
	interfaces = append(interfaces, protocol.SpectraInterface{
		Name: protocol.InterfaceCapabilities, Argv: []string{"capabilities", "--json"}, Output: protocol.OutputJSON,
		ResultSchema: &protocol.SchemaRef{Name: protocol.SchemaCapabilities, Version: protocol.CapabilitiesSchemaVersion},
	})
	return protocol.SpectraCapabilities{
		Schema:         protocol.SchemaRef{Name: protocol.SchemaCapabilities, Version: protocol.CapabilitiesSchemaVersion},
		SpectraVersion: spectraVersion,
		OS:             goos,
		Arch:           goarch,
		Interfaces:     interfaces,
	}
}

func schemaRef(name string) *protocol.SchemaRef {
	version, ok := ResultSchemaVersion(name)
	if !ok {
		panic("capabilities: unsupported result schema " + name)
	}
	return &protocol.SchemaRef{Name: name, Version: version}
}
