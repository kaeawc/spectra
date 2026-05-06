package rules

// CatalogSource loads rules from one catalog source. Built-in Go rules,
// filesystem YAML catalogs, and future remote catalogs all fit this shape.
type CatalogSource interface {
	Name() string
	LoadRules() ([]Rule, error)
}

// BuiltinCatalogSource loads the compiled V1 catalog.
type BuiltinCatalogSource struct{}

func (BuiltinCatalogSource) Name() string {
	return "built-in"
}

func (BuiltinCatalogSource) LoadRules() ([]Rule, error) {
	return V1Catalog(), nil
}

// YAMLFileCatalogSource loads CEL/YAML rules from local files.
type YAMLFileCatalogSource struct {
	Paths []string
}

func (s YAMLFileCatalogSource) Name() string {
	return "yaml-files"
}

func (s YAMLFileCatalogSource) LoadRules() ([]Rule, error) {
	if len(s.Paths) == 0 {
		return nil, nil
	}
	return LoadYAMLRules(s.Paths)
}

// LoadCatalog loads sources in order and rejects duplicate rule IDs across
// sources. Later metadata-only changes should use Overrides, not duplicate
// executable rules.
func LoadCatalog(sources ...CatalogSource) ([]Rule, error) {
	var catalog []Rule
	for _, source := range sources {
		rules, err := source.LoadRules()
		if err != nil {
			return nil, err
		}
		if len(catalog) == 0 {
			catalog = append([]Rule(nil), rules...)
			continue
		}
		catalog, err = MergeCatalogs(catalog, rules)
		if err != nil {
			return nil, err
		}
	}
	return catalog, nil
}
