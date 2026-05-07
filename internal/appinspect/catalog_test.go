package appinspect

import "testing"

type fakeComponent struct {
	id         string
	group      string
	kind       string
	attributes []Attribute
	operations []Operation
}

func (f fakeComponent) InspectID() string              { return f.id }
func (f fakeComponent) InspectGroup() string           { return f.group }
func (f fakeComponent) InspectKind() string            { return f.kind }
func (f fakeComponent) InspectAttributes() []Attribute { return f.attributes }
func (f fakeComponent) InspectOperations() []Operation { return f.operations }

func TestBuildCatalogGroupsAnyComponentDescriptor(t *testing.T) {
	got := BuildCatalog([]ComponentDescriptor{
		fakeComponent{id: "swift.scene.lifecycle", group: "swift", kind: "runtime"},
		fakeComponent{id: "java.lang:type=Memory", group: "java.lang", kind: "mbean"},
		fakeComponent{id: "java.lang:type=Threading", group: "java.lang", kind: "mbean", attributes: []Attribute{{Name: "ThreadCount"}, {Name: "DaemonThreadCount"}}},
	})
	if len(got.Groups) != 2 {
		t.Fatalf("groups = %#v", got.Groups)
	}
	if got.Groups[0].Name != "java.lang" || got.Groups[1].Name != "swift" {
		t.Fatalf("group order = %#v", got.Groups)
	}
	if got.Groups[0].Components[1].Attributes[0].Name != "DaemonThreadCount" {
		t.Fatalf("attributes not sorted: %#v", got.Groups[0].Components[1].Attributes)
	}
}

func TestWatchableAttributesKeepsReadableScalarsAcrossComponents(t *testing.T) {
	got := WatchableAttributes([]ComponentDescriptor{fakeComponent{
		id:    "java.lang:type=Threading",
		group: "java.lang",
		attributes: []Attribute{
			{Name: "ThreadCount", Type: "int", Readable: true},
			{Name: "ThreadIds", Type: "[J", Readable: true},
			{Name: "PeakThreadCount", Type: "java.lang.Integer", Readable: false},
			{Name: "Verbose", Type: "boolean", Readable: true},
		},
	}})
	if len(got) != 2 {
		t.Fatalf("watchable = %#v", got)
	}
	if got[0].Name != "java-lang-threadcount" || got[1].Name != "java-lang-verbose" {
		t.Fatalf("watch names = %#v", got)
	}
}

func TestWatchableAttributesAcceptsArchitectureSpecificTypeClassifier(t *testing.T) {
	got := WatchableAttributesWithOptions([]ComponentDescriptor{fakeComponent{
		id:    "electron.process",
		group: "electron",
		attributes: []Attribute{
			{Name: "RSS", Type: "number", Readable: true},
			{Name: "Command", Type: "string", Readable: true},
		},
	}}, WatchOptions{
		IsMetricType: func(typeName string) bool { return typeName == "number" },
		WatchName:    func(group, attribute string) string { return group + "." + attribute },
	})
	if len(got) != 1 {
		t.Fatalf("watchable = %#v", got)
	}
	if got[0].Name != "electron.RSS" || got[0].Component != "electron.process" {
		t.Fatalf("watch = %#v", got[0])
	}
}

func TestOperationSafetyClassifiesZeroArgAndParameterizedOps(t *testing.T) {
	if got := OperationSafetyFor(Operation{Name: "gc", Impact: OperationImpactAction}); !got.Invokable || got.Level != "mutating" {
		t.Fatalf("action safety = %#v", got)
	}
	if got := OperationSafetyFor(Operation{Name: "find", Parameters: []Parameter{{Type: "java.lang.String"}}}); got.Invokable || got.Level != "blocked" {
		t.Fatalf("parameterized safety = %#v", got)
	}
}
