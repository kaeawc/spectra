package capabilities

import protocol "github.com/kaeawc/spectra-protocol/protocol/v1"

func Build(spectraVersion, goos, goarch string) protocol.SpectraCapabilities {
	interfaces := []protocol.SpectraInterface{{Name: protocol.InterfaceVersion, Argv: []string{"version"}, Output: protocol.OutputText}}
	if goos == "darwin" {
		interfaces = append(interfaces, protocol.SpectraInterface{
			Name: protocol.InterfaceInspect, Argv: []string{"--json", "<app_path>..."}, Output: protocol.OutputJSON,
			ResultSchema: schemaRef(protocol.SchemaInspect),
		})
	}
	interfaces = append(interfaces,
		protocol.SpectraInterface{
			Name: protocol.InterfaceSnapshot, Argv: []string{"snapshot", "--json", "[--no-apps]"}, Output: protocol.OutputJSON,
			ResultSchema: schemaRef(protocol.SchemaSnapshot),
		},
		protocol.SpectraInterface{
			Name: protocol.InterfaceCapabilities, Argv: []string{"capabilities", "--json"}, Output: protocol.OutputJSON,
			ResultSchema: &protocol.SchemaRef{Name: protocol.SchemaCapabilities, Version: protocol.CapabilitiesSchemaVersion},
		},
	)
	return protocol.SpectraCapabilities{
		Schema:         protocol.SchemaRef{Name: protocol.SchemaCapabilities, Version: protocol.CapabilitiesSchemaVersion},
		SpectraVersion: spectraVersion,
		OS:             goos,
		Arch:           goarch,
		Interfaces:     interfaces,
	}
}

func schemaRef(name string) *protocol.SchemaRef {
	version, ok := protocol.SupportedResultSchemaVersion(name)
	if !ok {
		panic("capabilities: unsupported result schema " + name)
	}
	return &protocol.SchemaRef{Name: name, Version: version}
}
