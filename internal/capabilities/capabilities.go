package capabilities

const (
	InspectSchemaVersion      = 1
	SnapshotSchemaVersion     = 1
	CapabilitiesSchemaVersion = 1
)

type SchemaRef struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type Interface struct {
	Name         string     `json:"name"`
	Argv         []string   `json:"argv"`
	Output       string     `json:"output"`
	ResultSchema *SchemaRef `json:"result_schema,omitempty"`
}

type Manifest struct {
	Schema         SchemaRef   `json:"schema"`
	SpectraVersion string      `json:"spectra_version"`
	OS             string      `json:"os"`
	Arch           string      `json:"arch"`
	Interfaces     []Interface `json:"interfaces"`
}

func Build(spectraVersion, goos, goarch string) Manifest {
	return Manifest{
		Schema:         SchemaRef{Name: "spectra.capabilities", Version: CapabilitiesSchemaVersion},
		SpectraVersion: spectraVersion,
		OS:             goos,
		Arch:           goarch,
		Interfaces: []Interface{
			{Name: "version", Argv: []string{"version"}, Output: "text"},
			{Name: "inspect", Argv: []string{"--json", "<app_path>..."}, Output: "json", ResultSchema: &SchemaRef{Name: "spectra.inspect", Version: InspectSchemaVersion}},
			{Name: "snapshot", Argv: []string{"snapshot", "--json", "[--no-apps]"}, Output: "json", ResultSchema: &SchemaRef{Name: "spectra.snapshot", Version: SnapshotSchemaVersion}},
			{Name: "capabilities", Argv: []string{"capabilities", "--json"}, Output: "json", ResultSchema: &SchemaRef{Name: "spectra.capabilities", Version: CapabilitiesSchemaVersion}},
		},
	}
}
