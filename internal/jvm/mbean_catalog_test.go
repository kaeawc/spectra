package jvm

import "testing"

func TestCatalogMBeansGroupsAndSortsByDomain(t *testing.T) {
	got := CatalogMBeans(MBeansResult{MBeans: []MBean{
		{Name: "java.lang:type=Threading", Attributes: []MBeanAttribute{{Name: "ThreadCount"}, {Name: "DaemonThreadCount"}}},
		{Name: "com.example:name=Cache", Operations: []MBeanOperation{{Name: "reset", Impact: MBeanOperationImpactAction}}},
		{Name: "java.lang:type=Memory"},
	}})
	if len(got.Groups) != 2 {
		t.Fatalf("groups = %#v", got.Groups)
	}
	if got.Groups[0].Name != "com.example" || got.Groups[1].Name != "java.lang" {
		t.Fatalf("group order = %#v", got.Groups)
	}
	if got.Groups[1].Components[1].Attributes[0].Name != "DaemonThreadCount" {
		t.Fatalf("attributes not sorted: %#v", got.Groups[1].Components[1].Attributes)
	}
}

func TestWatchableMBeanAttributesKeepsReadableScalars(t *testing.T) {
	got := WatchableMBeanAttributes(MBeansResult{MBeans: []MBean{{
		Name: "java.lang:type=Threading",
		Attributes: []MBeanAttribute{
			{Name: "ThreadCount", Type: "int", Readable: true},
			{Name: "ThreadIds", Type: "[J", Readable: true},
			{Name: "PeakThreadCount", Type: "java.lang.Integer", Readable: false},
			{Name: "Verbose", Type: "boolean", Readable: true},
		},
	}}})
	if len(got) != 2 {
		t.Fatalf("watchable = %#v", got)
	}
	if got[0].Name != "java-lang-threadcount" || got[1].Name != "java-lang-verbose" {
		t.Fatalf("watch names = %#v", got)
	}
	if got[0].Component != "java.lang:type=Threading" {
		t.Fatalf("component = %q", got[0].Component)
	}
}

func TestOperationSafetyClassifiesZeroArgAndParameterizedOps(t *testing.T) {
	if got := OperationSafety(MBeanOperation{Name: "gc", Impact: MBeanOperationImpactAction}); !got.Invokable || got.Level != "mutating" {
		t.Fatalf("action safety = %#v", got)
	}
	if got := OperationSafety(MBeanOperation{Name: "find", Parameters: []MBeanParameter{{Type: "java.lang.String"}}}); got.Invokable || got.Level != "blocked" {
		t.Fatalf("parameterized safety = %#v", got)
	}
}
