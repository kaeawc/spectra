package rules

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLProfileSpec is the external profile format. Mirrors AppProfile but uses
// YAML naming conventions and is decoded with strict (KnownFields) parsing.
type YAMLProfileSpec struct {
	ID                string   `yaml:"id"`
	Name              string   `yaml:"name"`
	MainClassContains string   `yaml:"main_class_contains,omitempty"`
	JavaHomeContains  string   `yaml:"java_home_contains,omitempty"`
	VMArgsHas         []string `yaml:"vm_args_has,omitempty"`
	Tags              []string `yaml:"tags,omitempty"`
}

// LoadProfilesFromYAML parses zero or more YAML profile files. Each file may
// be a top-level list or {profiles: [...]}. Returns the merged list in input
// order; duplicate-ID handling is the caller's responsibility (use
// MergeProfiles to override builtins explicitly).
func LoadProfilesFromYAML(paths []string) ([]AppProfile, error) {
	var out []AppProfile
	for _, path := range paths {
		specs, err := loadProfileSpecs(path)
		if err != nil {
			return nil, err
		}
		for _, spec := range specs {
			profile, err := compileProfile(spec, path)
			if err != nil {
				return nil, err
			}
			out = append(out, profile)
		}
	}
	return out, nil
}

func loadProfileSpecs(path string) ([]YAMLProfileSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("%s: parse YAML: %w", path, err)
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	listNode, err := profilesListNode(root.Content[0])
	if err != nil {
		return nil, fmt.Errorf("%s:%d: %w", path, root.Content[0].Line, err)
	}
	var specs []YAMLProfileSpec
	for _, child := range listNode.Content {
		if err := rejectUnknownProfileFields(child); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, child.Line, err)
		}
		var spec YAMLProfileSpec
		if err := child.Decode(&spec); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, child.Line, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func rejectUnknownProfileFields(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("profile must be a mapping")
	}
	allowed := map[string]struct{}{
		"id":                  {},
		"name":                {},
		"main_class_contains": {},
		"java_home_contains":  {},
		"vm_args_has":         {},
		"tags":                {},
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown profile field %q", key)
		}
	}
	return nil
}

func profilesListNode(root *yaml.Node) (*yaml.Node, error) {
	switch root.Kind {
	case yaml.SequenceNode:
		return root, nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "profiles" {
				if root.Content[i+1].Kind != yaml.SequenceNode {
					return nil, fmt.Errorf("profiles must be a list")
				}
				return root.Content[i+1], nil
			}
		}
		return nil, fmt.Errorf("top-level mapping must contain profiles")
	default:
		return nil, fmt.Errorf("top-level YAML must be a profile list or {profiles: [...]}")
	}
}

func compileProfile(spec YAMLProfileSpec, source string) (AppProfile, error) {
	if strings.TrimSpace(spec.ID) == "" {
		return AppProfile{}, fmt.Errorf("%s: profile is missing id", source)
	}
	if spec.MainClassContains == "" && spec.JavaHomeContains == "" && len(spec.VMArgsHas) == 0 {
		return AppProfile{}, fmt.Errorf("%s: profile %q has no matchers (would never match)", source, spec.ID)
	}
	return AppProfile{
		ID:                spec.ID,
		Name:              spec.Name,
		MainClassContains: spec.MainClassContains,
		JavaHomeContains:  spec.JavaHomeContains,
		VMArgsHas:         append([]string(nil), spec.VMArgsHas...),
		Tags:              append([]string(nil), spec.Tags...),
	}, nil
}

// MergeProfiles returns base with extension merged in. Extension entries with
// IDs that already exist in base replace the base entries (user wins). Order
// is preserved: replaced entries keep their slot, new entries are appended.
func MergeProfiles(base, extension []AppProfile) []AppProfile {
	if len(extension) == 0 {
		return base
	}
	indexByID := make(map[string]int, len(base))
	out := append([]AppProfile(nil), base...)
	for i, p := range out {
		indexByID[p.ID] = i
	}
	for _, p := range extension {
		if idx, ok := indexByID[p.ID]; ok {
			out[idx] = p
			continue
		}
		indexByID[p.ID] = len(out)
		out = append(out, p)
	}
	return out
}

// ResolveProfileCatalog returns the effective profile catalog: builtin merged
// with profiles loaded from paths. Pass nil paths to get builtins only.
func ResolveProfileCatalog(paths []string) ([]AppProfile, error) {
	if len(paths) == 0 {
		return BuiltinProfiles(), nil
	}
	user, err := LoadProfilesFromYAML(paths)
	if err != nil {
		return nil, err
	}
	return MergeProfiles(BuiltinProfiles(), user), nil
}
