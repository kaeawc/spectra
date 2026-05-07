package appinspect

import (
	"sort"
	"strings"
)

const (
	OperationImpactInfo       = 0
	OperationImpactAction     = 1
	OperationImpactActionInfo = 2
	OperationImpactUnknown    = 3
)

type ComponentDescriptor interface {
	InspectID() string
	InspectGroup() string
	InspectKind() string
	InspectAttributes() []Attribute
	InspectOperations() []Operation
}

type Catalog struct {
	Groups []Group `json:"groups"`
}

type Group struct {
	Name       string      `json:"name"`
	Components []Component `json:"components"`
}

type Component struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind,omitempty"`
	Attributes []Attribute `json:"attributes,omitempty"`
	Operations []Operation `json:"operations,omitempty"`
}

type Attribute struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
}

type Operation struct {
	Name       string      `json:"name"`
	ReturnType string      `json:"return_type,omitempty"`
	Impact     int         `json:"impact,omitempty"`
	Parameters []Parameter `json:"parameters,omitempty"`
}

type Parameter struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

type WatchSpec struct {
	Name      string `json:"name,omitempty"`
	Component string `json:"component"`
	Attribute string `json:"attribute"`
	Type      string `json:"type,omitempty"`
}

type WatchOptions struct {
	IsMetricType func(string) bool
	WatchName    func(group, attribute string) string
}

type OperationSafety struct {
	Invokable bool   `json:"invokable"`
	Level     string `json:"level"`
	Reason    string `json:"reason"`
}

type Source interface {
	Components() ([]ComponentDescriptor, error)
}

type AttributeReader interface {
	ReadAttribute(component, attribute string) (any, error)
}

type OperationInvoker interface {
	InvokeOperation(component, operation string, args []any) (any, error)
}

func BuildCatalog(components []ComponentDescriptor) Catalog {
	byGroup := make(map[string][]Component)
	for _, desc := range components {
		component := Component{
			ID:         desc.InspectID(),
			Kind:       desc.InspectKind(),
			Attributes: sortedAttributes(desc.InspectAttributes()),
			Operations: sortedOperations(desc.InspectOperations()),
		}
		byGroup[desc.InspectGroup()] = append(byGroup[desc.InspectGroup()], component)
	}

	groups := make([]Group, 0, len(byGroup))
	for name, components := range byGroup {
		sort.SliceStable(components, func(i, j int) bool { return components[i].ID < components[j].ID })
		groups = append(groups, Group{Name: name, Components: components})
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return Catalog{Groups: groups}
}

func WatchableAttributes(components []ComponentDescriptor) []WatchSpec {
	return WatchableAttributesWithOptions(components, WatchOptions{})
}

func WatchableAttributesWithOptions(components []ComponentDescriptor, opts WatchOptions) []WatchSpec {
	isMetricType := opts.IsMetricType
	if isMetricType == nil {
		isMetricType = IsScalarMetricType
	}
	watchName := opts.WatchName
	if watchName == nil {
		watchName = DefaultWatchName
	}

	var specs []WatchSpec
	for _, component := range components {
		for _, attr := range component.InspectAttributes() {
			if !attr.Readable || !isMetricType(attr.Type) {
				continue
			}
			specs = append(specs, WatchSpec{
				Name:      watchName(component.InspectGroup(), attr.Name),
				Component: component.InspectID(),
				Attribute: attr.Name,
				Type:      attr.Type,
			})
		}
	}
	sort.SliceStable(specs, func(i, j int) bool {
		if specs[i].Component != specs[j].Component {
			return specs[i].Component < specs[j].Component
		}
		return specs[i].Attribute < specs[j].Attribute
	})
	return specs
}

func OperationSafetyFor(op Operation) OperationSafety {
	if len(op.Parameters) > 0 {
		return OperationSafety{Invokable: false, Level: "blocked", Reason: "operation requires parameters"}
	}
	switch op.Impact {
	case OperationImpactInfo:
		return OperationSafety{Invokable: true, Level: "read", Reason: "zero-argument informational operation"}
	case OperationImpactAction:
		return OperationSafety{Invokable: true, Level: "mutating", Reason: "zero-argument action operation"}
	case OperationImpactActionInfo:
		return OperationSafety{Invokable: true, Level: "mutating", Reason: "zero-argument action with informational return"}
	default:
		return OperationSafety{Invokable: true, Level: "unknown", Reason: "zero-argument operation with unknown impact"}
	}
}

func IsScalarMetricType(typeName string) bool {
	switch typeName {
	case "byte", "short", "int", "long", "float", "double",
		"java.lang.Byte", "java.lang.Short", "java.lang.Integer", "java.lang.Long", "java.lang.Float", "java.lang.Double",
		"boolean", "java.lang.Boolean":
		return true
	default:
		return false
	}
}

func DefaultWatchName(group, attribute string) string {
	if group == "" {
		group = "component"
	}
	return SanitizeName(group + "." + attribute)
}

func SanitizeName(in string) string {
	in = strings.ToLower(in)
	var b strings.Builder
	lastDash := false
	for _, r := range in {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func sortedAttributes(attrs []Attribute) []Attribute {
	out := append([]Attribute(nil), attrs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortedOperations(ops []Operation) []Operation {
	out := append([]Operation(nil), ops...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return len(out[i].Parameters) < len(out[j].Parameters)
	})
	return out
}
